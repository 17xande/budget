package auth

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The bootstrap: how a deployment with no users gets its first account. The server
// issues a one-time token on such a boot and prints it; the holder exchanges it at
// /setup for the first account. See the setup table's comment in internal/db for
// why that shape rather than a default password or an open first-run page.

var (
	// ErrSetupClosed means there is nothing to claim: no token was ever issued,
	// it has already been spent, or an account arrived by some other route.
	ErrSetupClosed = errors.New("auth: setup has already been completed")
	// ErrBadSetupToken means the token offered is not the one issued.
	ErrBadSetupToken = errors.New("auth: setup token does not match")
)

// CountUsers is how many accounts exist. The boot path uses it to decide whether
// the deployment still needs bootstrapping.
func (s *Store) CountUsers() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// CreateSetupToken records the hash of the token that may claim the first account,
// reporting whether it stored this one.
//
// False means a token was already there. The table holds a single row, so a second
// caller conflicts rather than replacing a live token -- which makes this safe on
// every boot: a browser may be sitting on a half-finished setup page holding the
// first one.
func (s *Store) CreateSetupToken(token string, now time.Time) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO setup (id, token_hash, created_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO NOTHING`, hashToken(token), formatTime(now))
	if err != nil {
		return false, fmt.Errorf("create setup token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected creating setup token: %w", err)
	}
	return n > 0, nil
}

// SetupPending reports whether the deployment is still claimable. It asks exactly
// what ClaimSetup enforces, in one place so the two cannot disagree: should a first
// account ever arrive by another route, an unspent token must not keep offering a
// claim whose every submission fails.
func (s *Store) SetupPending() (bool, error) {
	var pending bool
	err := s.db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM setup
			WHERE id = 1 AND consumed_at = '' AND NOT EXISTS (SELECT 1 FROM users)
		)`).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("setup pending: %w", err)
	}
	return pending, nil
}

// CheckSetupToken reports whether token is the issued, unspent setup token. The
// boot path uses it to tell whether a SETUP_TOKEN in the environment will work.
func (s *Store) CheckSetupToken(token string) (bool, error) {
	var storedHash, consumedAt string
	err := s.db.QueryRow(`SELECT token_hash, consumed_at FROM setup WHERE id = 1`).Scan(&storedHash, &consumedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read setup token: %w", err)
	}
	return consumedAt == "" && tokenMatches(storedHash, token), nil
}

// ClaimSetup spends the setup token and creates the first account, in one
// transaction.
//
// The guard is in the UPDATE's WHERE rather than only in the read before it: of two
// requests arriving with the same token at once, exactly one may get a row. That
// holds because SQLite takes a database-wide write lock (and Open asks for BEGIN
// IMMEDIATE), not because of the SQL -- do not carry this shape to Postgres
// unchanged.
func (s *Store) ClaimSetup(token, email, name, passwordHash string, now time.Time) (User, error) {
	if err := ParsePasswordHash(passwordHash); err != nil {
		return User{}, fmt.Errorf("claim setup: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, fmt.Errorf("begin claim setup: %w", err)
	}
	defer tx.Rollback()

	var storedHash, consumedAt string
	err = tx.QueryRow(`SELECT token_hash, consumed_at FROM setup WHERE id = 1`).Scan(&storedHash, &consumedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return User{}, ErrSetupClosed
	case err != nil:
		return User{}, fmt.Errorf("read setup token: %w", err)
	}
	if consumedAt != "" {
		return User{}, ErrSetupClosed
	}
	// Checked before the consume, so a wrong token is told apart from a closed
	// setup -- they are different messages to the person at the form.
	if !tokenMatches(storedHash, token) {
		return User{}, ErrBadSetupToken
	}

	res, err := tx.Exec(`
		UPDATE setup SET consumed_at = ?
		WHERE id = 1 AND consumed_at = '' AND NOT EXISTS (SELECT 1 FROM users)`, formatTime(now))
	if err != nil {
		return User{}, fmt.Errorf("consume setup token: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return User{}, fmt.Errorf("rows affected consuming setup token: %w", err)
	} else if n == 0 {
		return User{}, ErrSetupClosed
	}

	// The first account chose its own password, so it is not forced to change it.
	u, err := createUser(tx, email, name, passwordHash, false, now)
	if err != nil {
		return User{}, err
	}
	// Claiming signs you straight in, so it is a sign-in: without this the
	// members page says you have "never" signed in while you are looking at it.
	if _, err := tx.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, formatTime(now), u.ID); err != nil {
		return User{}, fmt.Errorf("record claim sign-in: %w", err)
	}
	u.LastLoginAt = now.UTC()
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit claim setup: %w", err)
	}
	return u, nil
}

// tokenMatches compares a stored token hash against a candidate in constant time.
func tokenMatches(storedHash, token string) bool {
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashToken(token))) == 1
}

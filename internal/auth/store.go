package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// User is someone who can sign in. There are no roles: a household budget has
// two people who both see and do everything.
type User struct {
	ID    int64
	Email string
	Name  string
	// MustChangePassword is set when somebody else chose this password -- every
	// member added from the members page -- and forces the next sign-in through
	// the change form before anything else opens.
	MustChangePassword bool
	CreatedAt          time.Time
	LastLoginAt        time.Time // zero if they have never signed in
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// dummyHash is verified against when no user matches, so that an unknown email
// and a wrong password take the same time. Without it, whether an account exists
// is readable straight off the response time: a miss returns immediately while a
// hit spends a tenth of a second in argon2.
var dummyHash = func() string {
	h, err := HashPassword("not a real password", DefaultParams)
	if err != nil {
		// Only fails on an empty password or incomplete params; neither is
		// possible here.
		panic("auth: cannot build dummy hash: " + err.Error())
	}
	return h
}()

const userColumns = `id, email, name, must_change_password, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }, extra ...any) (User, error) {
	var u User
	dest := append([]any{&u.ID, &u.Email, &u.Name, &u.MustChangePassword,
		timeScan(&u.CreatedAt), timeScan(&u.LastLoginAt)}, extra...)
	return u, row.Scan(dest...)
}

// Authenticate verifies an email and password. The bool is false for an unknown
// email or a wrong password -- the caller must not distinguish them to the visitor.
//
// On success it records the login time, which is what tells you whether an
// account is still in use.
func (s *Store) Authenticate(email, password string, now time.Time) (User, bool, error) {
	var hash string
	u, err := scanUser(s.db.QueryRow(`SELECT `+userColumns+`, password_hash FROM users WHERE email = ?`, email), &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Spend the same time as a real verification before answering.
		CheckPassword(dummyHash, password)
		return User{}, false, nil
	case err != nil:
		return User{}, false, fmt.Errorf("find user: %w", err)
	}
	if !CheckPassword(hash, password) {
		return User{}, false, nil
	}
	if _, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, formatTime(now), u.ID); err != nil {
		// The sign-in succeeded; failed bookkeeping is not worth refusing it over.
		return u, true, nil
	}
	u.LastLoginAt = now.UTC()
	return u, true, nil
}

// ErrEmailTaken is returned when the email is already in use, so a caller can turn
// it into a field error rather than matching a driver's message text.
var ErrEmailTaken = errors.New("auth: a user with this email already exists")

// execer is the part of *sql.DB and *sql.Tx that createUser needs, so one insert
// serves both CreateUser and ClaimSetup -- the latter runs inside the transaction
// that spends the setup token.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// CreateUser inserts a user and returns it. mustChange marks the password as
// somebody else's choice.
func (s *Store) CreateUser(email, name, passwordHash string, mustChange bool, now time.Time) (User, error) {
	return createUser(s.db, email, name, passwordHash, mustChange, now)
}

// createUser refuses a duplicate email with the insert itself (ON CONFLICT DO
// NOTHING, then RowsAffected) rather than a preceding lookup: a find-then-insert
// pair races, and email is the identity of an account. The column is COLLATE
// NOCASE, matching how Authenticate looks it up, so "A@x" and "a@x" are one person.
func createUser(q execer, email, name, passwordHash string, mustChange bool, now time.Time) (User, error) {
	if email == "" {
		return User{}, errors.New("create user: email must not be empty")
	}
	if err := ParsePasswordHash(passwordHash); err != nil {
		return User{}, fmt.Errorf("create user %s: %w", email, err)
	}
	res, err := q.Exec(`
		INSERT INTO users (email, name, password_hash, must_change_password, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(email) DO NOTHING`, email, name, passwordHash, mustChange, formatTime(now))
	if err != nil {
		return User{}, fmt.Errorf("insert user %s: %w", email, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return User{}, fmt.Errorf("rows affected for user %s: %w", email, err)
	}
	if n == 0 {
		return User{}, fmt.Errorf("create user %s: %w", email, ErrEmailTaken)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("user last insert id: %w", err)
	}
	return User{ID: id, Email: email, Name: name, MustChangePassword: mustChange, CreatedAt: now.UTC()}, nil
}

// Users returns everyone, ordered by name.
func (s *Store) Users() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY name, email`)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// FindByEmail returns one user. The bool is false if there is no such user.
func (s *Store) FindByEmail(email string) (User, bool, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE email = ?`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("find user %s: %w", email, err)
	}
	return u, true, nil
}

// CheckUserPassword reports whether password is the current one for id.
//
// Separate from Authenticate because confirming your own password before changing
// it is not a sign-in, and recording it as one would make last_login_at lie.
func (s *Store) CheckUserPassword(id int64, password string) (bool, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read password hash for user %d: %w", id, err)
	}
	return CheckPassword(hash, password), nil
}

// SetPassword replaces a user's password and deletes all their sessions and API
// tokens, reporting whether the user exists.
//
// The two go together: a password change that left existing sessions alive would
// not actually shut anyone out, which is the main reason someone changes one.
func (s *Store) SetPassword(id int64, passwordHash string, mustChange bool) (bool, error) {
	if err := ParsePasswordHash(passwordHash); err != nil {
		return false, fmt.Errorf("set password for user %d: %w", id, err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin set password: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE users SET password_hash = ?, must_change_password = ? WHERE id = ?`,
		passwordHash, mustChange, id)
	if err != nil {
		return false, fmt.Errorf("update password for user %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return false, fmt.Errorf("clear sessions for user %d: %w", id, err)
	}
	// API tokens too, in the same transaction: an AI client holding one is as
	// much "someone with the old password" as a browser holding a session.
	if _, err := tx.Exec(`DELETE FROM api_tokens WHERE user_id = ?`, id); err != nil {
		return false, fmt.Errorf("clear api tokens for user %d: %w", id, err)
	}
	return true, tx.Commit()
}

// tokenBytes is the entropy in a session or setup token: far past guessable, and
// the token is never displayed, so there is no reason to be frugal.
const tokenBytes = 32

// NewToken returns a fresh random token, base64url without padding so it needs no
// escaping in a cookie or a form field.
func NewToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// IssueSession creates a session for userID and returns the token for the cookie.
// Only the token's sha256 is stored.
func (s *Store) IssueSession(userID int64, now time.Time, ttl time.Duration) (string, error) {
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`
		INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, formatTime(now), formatTime(now.Add(ttl))); err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}
	return token, nil
}

// VerifySession returns the user a session token belongs to. The bool is false if
// the token is unknown or expired.
func (s *Store) VerifySession(token string, now time.Time) (User, bool, error) {
	if token == "" {
		return User{}, false, nil
	}
	var expires time.Time
	u, err := scanUser(s.db.QueryRow(`
		SELECT u.id, u.email, u.name, u.must_change_password, u.created_at, u.last_login_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, hashToken(token)), timeScan(&expires))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return User{}, false, nil
	case err != nil:
		return User{}, false, fmt.Errorf("verify session: %w", err)
	}
	if !now.Before(expires) {
		return User{}, false, nil
	}
	return u, true, nil
}

// DeleteSession removes one session, for signing out.
func (s *Store) DeleteSession(token string) error {
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions that have run out and returns how many.
// Expiry is already enforced on read, so this is housekeeping: without it the
// table grows for ever.
func (s *Store) DeleteExpiredSessions(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// hashToken is what gets stored. sha256 rather than a password hash: the token is
// 32 bytes of uniform randomness, so there is nothing to brute-force, and a slow
// KDF here would be paid on every request.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// formatTime is how every timestamp is stored: RFC3339 in UTC. Fixed-width UTC
// strings also compare correctly as text, which DeleteExpiredSessions relies on.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// timeScan adapts a TEXT column into a time.Time, treating "" as the zero time --
// which is what last_login_at holds for someone who has never signed in.
func timeScan(dst *time.Time) sql.Scanner {
	return timeScanner{dst}
}

type timeScanner struct{ dst *time.Time }

func (t timeScanner) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("auth: cannot scan %T as a time", src)
	}
	if s == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("auth: parse time %q: %w", s, err)
	}
	*t.dst = parsed
	return nil
}

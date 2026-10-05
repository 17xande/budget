package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// APITokenPrefix starts every API token, so one pasted into a chat, a commit or a
// log is recognisable for what it is -- by a person or a secret scanner.
const APITokenPrefix = "bgt_"

// MaxAPITokenTTL is the longest a token may live. A credential that never
// expires is one nobody remembers to revoke.
const MaxAPITokenTTL = 366 * 24 * time.Hour

// APIToken is a token's record. It never holds the token itself: that exists only
// in what IssueAPIToken returned, once.
type APIToken struct {
	ID         int64
	UserID     int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt time.Time // zero: never used
	ExpiresAt  time.Time
}

var (
	ErrInvalidTokenName = errors.New("auth: a token needs a name of 1 to 60 characters")
	ErrTokenNotFound    = errors.New("auth: no such token")
)

// IssueAPIToken creates a token for a user and returns it with its record. The
// token is returned this once; only its hash is kept.
func (s *Store) IssueAPIToken(userID int64, name string, ttl time.Duration, now time.Time) (string, APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 60 {
		return "", APIToken{}, ErrInvalidTokenName
	}
	if ttl <= 0 || ttl > MaxAPITokenTTL {
		return "", APIToken{}, fmt.Errorf("auth: api token lifetime must be between 0 and %s, got %s", MaxAPITokenTTL, ttl)
	}
	raw, err := NewToken()
	if err != nil {
		return "", APIToken{}, err
	}
	token := APITokenPrefix + raw
	rec := APIToken{UserID: userID, Name: name, CreatedAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC()}
	res, err := s.db.Exec(`INSERT INTO api_tokens (user_id, name, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		userID, name, hashToken(token), formatTime(rec.CreatedAt), formatTime(rec.ExpiresAt))
	if err != nil {
		return "", APIToken{}, fmt.Errorf("insert api token: %w", err)
	}
	rec.ID, _ = res.LastInsertId()
	return token, rec, nil
}

// APITokenUser looks a bearer token up and returns the user it acts as.
//
// ErrTokenNotFound is the one answer for a token that is malformed, unknown,
// expired, or whose user must change their password: none of them is "nearly
// authenticated", and telling them apart helps only someone guessing.
func (s *Store) APITokenUser(token string, now time.Time) (APIToken, User, error) {
	// Never look up a value nobody could have been issued.
	if !strings.HasPrefix(token, APITokenPrefix) || len(token) == len(APITokenPrefix) {
		return APIToken{}, User{}, ErrTokenNotFound
	}
	var rec APIToken
	var hash string
	u, err := scanUser(s.db.QueryRow(`
		SELECT u.id, u.email, u.name, u.must_change_password, u.created_at, u.last_login_at,
		       t.id, t.name, t.created_at, t.last_used_at, t.expires_at, t.token_hash
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, hashToken(token)),
		&rec.ID, &rec.Name, timeScan(&rec.CreatedAt), timeScan(&rec.LastUsedAt), timeScan(&rec.ExpiresAt), &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return APIToken{}, User{}, ErrTokenNotFound
	case err != nil:
		return APIToken{}, User{}, fmt.Errorf("find api token: %w", err)
	}
	rec.UserID = u.ID
	// A password somebody else chose has not yet been taken over by its owner;
	// nothing acting as that account should work until it has.
	if !now.Before(rec.ExpiresAt) || u.MustChangePassword {
		return APIToken{}, User{}, ErrTokenNotFound
	}
	return rec, u, nil
}

// TouchAPIToken records use, at most once an hour: "last used" is for spotting
// a forgotten token, not an access log, and a write on every call is a cost for
// nothing.
func (s *Store) TouchAPIToken(id int64, now time.Time) error {
	_, err := s.db.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at = '' OR last_used_at < ?)`,
		formatTime(now), id, formatTime(now.Add(-time.Hour)))
	return err
}

// APITokens returns a user's tokens, newest first.
func (s *Store) APITokens(userID int64) ([]APIToken, error) {
	rows, err := s.db.Query(`SELECT id, user_id, name, created_at, last_used_at, expires_at
		FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, timeScan(&t.CreatedAt), timeScan(&t.LastUsedAt), timeScan(&t.ExpiresAt)); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken deletes one of a user's tokens. The owner is part of the
// statement, so one person cannot revoke (or probe for) another's token by id.
func (s *Store) RevokeAPIToken(userID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/17xande/budget/internal/auth"
)

func TestAPITokens(t *testing.T) {
	s := newStore(t)
	u, _ := s.CreateUser("a@example.com", "A", hash(t, "correct horse battery"), false, t0)
	other, _ := s.CreateUser("b@example.com", "B", hash(t, "correct horse battery"), false, t0)

	tok, rec, err := s.IssueAPIToken(u.ID, "laptop", 30*24*time.Hour, t0)
	if err != nil || !strings.HasPrefix(tok, auth.APITokenPrefix) {
		t.Fatalf("issue: %q %v", tok, err)
	}
	got, user, err := s.APITokenUser(tok, t0.Add(time.Hour))
	if err != nil || user.ID != u.ID || got.ID != rec.ID || got.Name != "laptop" {
		t.Fatalf("lookup: %+v %+v %v", got, user, err)
	}
	for name, bad := range map[string]string{"unknown": auth.APITokenPrefix + "nope", "no prefix": "abc", "bare prefix": auth.APITokenPrefix, "empty": ""} {
		if _, _, err := s.APITokenUser(bad, t0); !errors.Is(err, auth.ErrTokenNotFound) {
			t.Errorf("%s token: %v", name, err)
		}
	}
	if _, _, err := s.APITokenUser(tok, t0.Add(31*24*time.Hour)); !errors.Is(err, auth.ErrTokenNotFound) {
		t.Errorf("expired token accepted: %v", err)
	}

	// Revocation is scoped to the owner.
	if err := s.RevokeAPIToken(other.ID, rec.ID); !errors.Is(err, auth.ErrTokenNotFound) {
		t.Errorf("another user revoked my token: %v", err)
	}
	if err := s.RevokeAPIToken(u.ID, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.APITokenUser(tok, t0); !errors.Is(err, auth.ErrTokenNotFound) {
		t.Error("revoked token still works")
	}

	if _, _, err := s.IssueAPIToken(u.ID, "  ", time.Hour, t0); !errors.Is(err, auth.ErrInvalidTokenName) {
		t.Errorf("blank name: %v", err)
	}
	if _, _, err := s.IssueAPIToken(u.ID, "x", auth.MaxAPITokenTTL+time.Hour, t0); err == nil {
		t.Error("an over-long lifetime was accepted")
	}
}

// Changing a password is how you shut out whoever has your old one -- including
// an AI client holding a token made with it.
func TestPasswordChangeRevokesTokens(t *testing.T) {
	s := newStore(t)
	u, _ := s.CreateUser("a@example.com", "A", hash(t, "correct horse battery"), false, t0)
	tok, _, _ := s.IssueAPIToken(u.ID, "laptop", time.Hour, t0)
	s.SetPassword(u.ID, hash(t, "a new passphrase!"), false)
	if _, _, err := s.APITokenUser(tok, t0); !errors.Is(err, auth.ErrTokenNotFound) {
		t.Fatal("a token survived a password change")
	}
}

// A token for an account on a temporary password does not work until its owner
// has chosen their own.
func TestMustChangeBlocksTokens(t *testing.T) {
	s := newStore(t)
	u, _ := s.CreateUser("a@example.com", "A", hash(t, "correct horse battery"), true, t0)
	tok, _, _ := s.IssueAPIToken(u.ID, "x", time.Hour, t0)
	if _, _, err := s.APITokenUser(tok, t0); !errors.Is(err, auth.ErrTokenNotFound) {
		t.Fatal("a must-change account's token worked")
	}
}

func TestTouchIsThrottled(t *testing.T) {
	s := newStore(t)
	u, _ := s.CreateUser("a@example.com", "A", hash(t, "correct horse battery"), false, t0)
	tok, rec, _ := s.IssueAPIToken(u.ID, "x", 24*time.Hour, t0)
	s.TouchAPIToken(rec.ID, t0.Add(time.Minute))
	s.TouchAPIToken(rec.ID, t0.Add(2*time.Minute)) // within the hour: ignored
	got, _, _ := s.APITokenUser(tok, t0)
	if !got.LastUsedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("last used %v", got.LastUsedAt)
	}
}

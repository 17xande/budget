package main

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17xande/budget/internal/auth"
	"github.com/17xande/budget/internal/db"
)

func newStore(t *testing.T) *auth.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return auth.NewStore(d)
}

func TestEnsureSetupTokenPrintsGeneratedToken(t *testing.T) {
	s := newStore(t)
	var buf bytes.Buffer
	if err := ensureSetupToken(s, "", log.New(&buf, "", 0)); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	i := strings.LastIndex(line, ": ")
	if i < 0 {
		t.Fatalf("no token in log line %q", line)
	}
	token := strings.TrimSpace(line[i+2:])
	// The printed token must be the one that actually works.
	if ok, _ := s.CheckSetupToken(token); !ok {
		t.Fatalf("logged token %q does not claim setup", token)
	}

	// A restart must not issue a second token or reprint anything usable.
	buf.Reset()
	ensureSetupToken(s, "", log.New(&buf, "", 0))
	if strings.Contains(buf.String(), token) {
		t.Error("a restart reprinted the token")
	}
	if ok, _ := s.CheckSetupToken(token); !ok {
		t.Error("a restart invalidated the first token")
	}
}

func TestEnsureSetupTokenWarnsOnMismatchedSupplied(t *testing.T) {
	s := newStore(t)
	ensureSetupToken(s, "", log.New(&bytes.Buffer{}, "", 0))
	var buf bytes.Buffer
	ensureSetupToken(s, strings.Repeat("x", 40), log.New(&buf, "", 0))
	if !strings.Contains(buf.String(), "does not match") {
		t.Errorf("no warning for a SETUP_TOKEN that cannot work: %q", buf.String())
	}
}

func TestEnsureSetupTokenSilentOnceClaimed(t *testing.T) {
	s := newStore(t)
	h, _ := auth.HashPassword("correct horse battery", auth.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	s.CreateUser("a@example.com", "A", h, false, time.Now())
	var buf bytes.Buffer
	ensureSetupToken(s, "", log.New(&buf, "", 0))
	if buf.Len() != 0 {
		t.Errorf("logged on a claimed deployment: %q", buf.String())
	}
}

func TestResetPassword(t *testing.T) {
	s := newStore(t)
	h, _ := auth.HashPassword("the old passphrase", auth.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	u, _ := s.CreateUser("a@example.com", "A", h, true, time.Now())
	tok, _ := s.IssueSession(u.ID, time.Now(), time.Hour)

	var out bytes.Buffer
	if err := resetPassword(s, "A@example.com", strings.NewReader("a brand new passphrase\n"), &out); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.Authenticate("a@example.com", "a brand new passphrase", time.Now())
	if !ok {
		t.Fatal("new password does not work")
	}
	if got.MustChangePassword {
		t.Error("a password the owner set themselves is marked must-change")
	}
	if _, ok, _ := s.VerifySession(tok, time.Now()); ok {
		t.Error("an old session survived a reset")
	}

	// Same policy as the web forms.
	if err := resetPassword(s, "a@example.com", strings.NewReader("short\n"), &out); err == nil {
		t.Error("a short password was accepted")
	}
	// Never creates an account.
	if err := resetPassword(s, "nobody@example.com", strings.NewReader("a brand new passphrase\n"), &out); err == nil {
		t.Error("reset for an unknown address succeeded")
	}
}

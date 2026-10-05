package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.CookieSecure {
		t.Error("cookies must default to Secure")
	}
	if c.SessionTTL != 30*24*time.Hour || c.Port != "8080" || c.DBPath != "data/budget.db" {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

// A typo must not quietly mean "insecure".
func TestCookieSecureIsStrict(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "ture")
	if _, err := Load(); err == nil {
		t.Fatal("COOKIE_SECURE=ture was accepted")
	}
	t.Setenv("COOKIE_SECURE", "false")
	if c, err := Load(); err != nil || c.CookieSecure {
		t.Fatalf("COOKIE_SECURE=false: secure=%v err=%v", c.CookieSecure, err)
	}
}

func TestBadSessionTTL(t *testing.T) {
	t.Setenv("SESSION_TTL", "-1h")
	if _, err := Load(); err == nil {
		t.Fatal("a negative session TTL was accepted")
	}
}

func TestShortSetupTokenIgnored(t *testing.T) {
	t.Setenv("SETUP_TOKEN", "short")
	c, _ := Load()
	if c.SetupToken != "" {
		t.Error("a short SETUP_TOKEN was kept")
	}
	t.Setenv("SETUP_TOKEN", "  "+strings.Repeat("x", MinSetupTokenLength)+"\n")
	c, _ = Load()
	if c.SetupToken != strings.Repeat("x", MinSetupTokenLength) {
		t.Errorf("a long SETUP_TOKEN was not kept trimmed: %q", c.SetupToken)
	}
}

func TestTrustedProxies(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", " 172.16.0.0/12, ,10.0.0.0/8")
	c, _ := Load()
	if len(c.TrustedProxies) != 2 {
		t.Errorf("got %v", c.TrustedProxies)
	}
}

func TestFNB(t *testing.T) {
	dir := t.TempDir()
	secret := dir + "/fnb_password"
	os.WriteFile(secret, []byte("hunter2-not-really\n"), 0o600)

	t.Setenv("FNB_USERNAME", "someone")
	t.Setenv("FNB_PASSWORD_FILE", secret)
	t.Setenv("FETCH_AT", "05:30")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.FNB.Password != "hunter2-not-really" || c.FNB.Hour != 5 || c.FNB.Minute != 30 || len(c.FNB.Products) != 2 {
		t.Errorf("fnb config: %+v", c.FNB)
	}

	// A password in a plain environment variable is refused, not used.
	t.Setenv("FNB_PASSWORD", "plain")
	if _, err := Load(); err == nil {
		t.Error("FNB_PASSWORD was accepted")
	}
	t.Setenv("FNB_PASSWORD", "")

	// Half-configured is a startup error.
	t.Setenv("FNB_PASSWORD_FILE", "")
	if _, err := Load(); err == nil {
		t.Error("a username without a password was accepted")
	}
	t.Setenv("FNB_PASSWORD_FILE", secret)
	t.Setenv("FETCH_AT", "6am")
	if _, err := Load(); err == nil {
		t.Error("FETCH_AT=6am was accepted")
	}
}

package config

import (
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

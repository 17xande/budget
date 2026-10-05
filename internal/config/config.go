// Package config reads the environment into one Config. Nothing else in the
// program reads environment variables.
package config

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type Config struct {
	Port   string
	DBPath string

	// CookieSecure marks the session cookie Secure. True by default; set false
	// only for plain-http local development. It doubles as the "is this https"
	// signal for the CSRF origin check, because behind a TLS-terminating proxy
	// the connection reaching us is plain http while the browser's is not.
	CookieSecure bool

	// SessionTTL is how long a sign-in lasts. Two people on their own devices,
	// so the default is long; a password change ends every session regardless.
	SessionTTL time.Duration

	// TrustedProxies lists the CIDRs of proxies (Traefik) allowed to set
	// X-Forwarded-For. Empty trusts nothing: every request is keyed on the raw
	// peer address, which behind a proxy means everyone shares one rate-limit
	// bucket. Safe, just coarse.
	TrustedProxies []string

	// SetupToken supplies the one-time token that claims the first account, for a
	// deploy that cannot conveniently read the token otherwise printed on first
	// boot. Empty -- the normal case -- means one is generated and logged.
	SetupToken string
}

// MinSetupTokenLength is the shortest SETUP_TOKEN accepted. The token is all that
// stands between a fresh deployment and whoever reaches it first, so a short one
// is worse than none: whoever set it believes it protects them.
const MinSetupTokenLength = 32

func Load() (Config, error) {
	c := Config{
		Port:           getenv("PORT", "8080"),
		DBPath:         getenv("DB_PATH", "data/budget.db"),
		TrustedProxies: parseList(os.Getenv("TRUSTED_PROXIES")),
		SetupToken:     strings.TrimSpace(os.Getenv("SETUP_TOKEN")),
	}

	switch v := getenv("COOKIE_SECURE", "true"); v {
	case "true":
		c.CookieSecure = true
	case "false":
	default:
		// Strict, because the permissive reading of a typo ("ture") is an
		// insecure cookie on a deployment that meant to be https.
		return Config{}, fmt.Errorf("config: COOKIE_SECURE must be true or false, got %q", v)
	}

	ttl, err := duration("SESSION_TTL", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	c.SessionTTL = ttl

	if c.SetupToken != "" && len(c.SetupToken) < MinSetupTokenLength {
		// Discarded rather than fatal: a generated token is printed instead, so
		// the deployment is still claimable by whoever can read the log -- the
		// safe direction. Loud, so nobody goes on believing theirs is in force.
		log.Printf("SETUP_TOKEN is %d characters, want at least %d; ignoring it and issuing a "+
			"generated token instead (make one with `openssl rand -base64 32`)",
			len(c.SetupToken), MinSetupTokenLength)
		c.SetupToken = ""
	}
	return c, nil
}

func duration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive duration like 720h, got %q", key, v)
	}
	return d, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

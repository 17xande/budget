// Package config reads the environment into one Config. Nothing else in the
// program reads environment variables.
package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/middleware"
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

	// ClientIPSource says where the address a request came from is read from, for
	// the rate limits: remote (the TCP peer; the default and the safe one, but
	// behind a proxy every client then shares one bucket), forwarded (leftmost
	// X-Forwarded-For) or cloudflare (CF-Connecting-IP). Set it to what is true of
	// the deployment; see middleware.ClientIPSource.
	ClientIPSource middleware.ClientIPSource

	// BackupDir receives a daily copy of the database; BackupKeep is how many
	// days are kept there. BackupKeep 0 turns backups off.
	BackupDir  string
	BackupKeep int

	// FNB is the daily fetch's configuration. With no username it is off.
	FNB FNB

	// SetupToken supplies the one-time token that claims the first account, for a
	// deploy that cannot conveniently read the token otherwise printed on first
	// boot. Empty -- the normal case -- means one is generated and logged.
	SetupToken string
}

// FNB configures the automated fetch.
type FNB struct {
	Username string
	// Password is read from the file FNB_PASSWORD_FILE names (a docker secret),
	// never from a plain environment variable: those end up in `docker inspect`,
	// in process listings and in crash dumps.
	Password string
	// Profile is the exact name of the profile to choose when the login is
	// linked to several (FNB's "Linked Profiles" page).
	Profile string
	// ChromeURL is the headless Chrome's DevTools endpoint.
	ChromeURL string
	// Hour and Minute are the daily run time in South Africa (FETCH_AT, HH:MM).
	Hour, Minute int
	// Products are the FNB product codes fetched.
	Products []string
}

// MinSetupTokenLength is the shortest SETUP_TOKEN accepted. The token is all that
// stands between a fresh deployment and whoever reaches it first, so a short one
// is worse than none: whoever set it believes it protects them.
const MinSetupTokenLength = 32

func Load() (Config, error) {
	c := Config{
		Port:       getenv("PORT", "8080"),
		DBPath:     getenv("DB_PATH", "data/budget.db"),
		SetupToken: strings.TrimSpace(os.Getenv("SETUP_TOKEN")),
	}

	// Refused rather than ignored: the old setting trusted forwarding headers from
	// a CIDR list, and silently dropping it would leave a deployment that set it
	// believing it still had per-client limits.
	if _, ok := os.LookupEnv("TRUSTED_PROXIES"); ok {
		return Config{}, errors.New("config: TRUSTED_PROXIES has been replaced by CLIENT_IP_SOURCE " +
			"(remote, forwarded or cloudflare); unset it, and set CLIENT_IP_SOURCE=cloudflare only behind the Cloudflare tunnel")
	}
	src, err := middleware.ParseClientIPSource(getenv("CLIENT_IP_SOURCE", string(middleware.ClientIPRemote)))
	if err != nil {
		return Config{}, fmt.Errorf("config: CLIENT_IP_SOURCE: %w", err)
	}
	c.ClientIPSource = src

	switch v := getenv("COOKIE_SECURE", "true"); v {
	case "true":
		c.CookieSecure = true
	case "false":
	default:
		// Strict, because the permissive reading of a typo ("ture") is an
		// insecure cookie on a deployment that meant to be https.
		return Config{}, fmt.Errorf("config: COOKIE_SECURE must be true or false, got %q", v)
	}

	// Beside the database by default, so the one volume holds both; that guards
	// against a bad write or a mistaken delete, not a lost disk -- see README.
	c.BackupDir = getenv("BACKUP_DIR", filepath.Join(filepath.Dir(c.DBPath), "backups"))
	keep := getenv("BACKUP_KEEP", "14")
	if n, err := strconv.Atoi(keep); err != nil || n < 0 {
		return Config{}, fmt.Errorf("config: BACKUP_KEEP must be a whole number of days (0 for none), got %q", keep)
	} else {
		c.BackupKeep = n
	}

	if err := loadFNB(&c.FNB); err != nil {
		return Config{}, err
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

func loadFNB(f *FNB) error {
	f.Username = strings.TrimSpace(os.Getenv("FNB_USERNAME"))
	f.Profile = strings.TrimSpace(os.Getenv("FNB_PROFILE"))
	f.ChromeURL = getenv("CHROME_URL", "ws://chrome:9222/")
	f.Products = parseList(getenv("FNB_PRODUCTS", "DDA,ZFN"))
	if os.Getenv("FNB_PASSWORD") != "" {
		return fmt.Errorf("config: FNB_PASSWORD is not read -- put the password in a file (a docker secret) and set FNB_PASSWORD_FILE to its path")
	}
	if path := os.Getenv("FNB_PASSWORD_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config: FNB_PASSWORD_FILE: %w", err)
		}
		f.Password = strings.TrimRight(string(b), "\r\n")
	}
	if (f.Username == "") != (f.Password == "") {
		// Half-configured is the case to catch at startup, not at 06:00.
		return fmt.Errorf("config: FNB_USERNAME and FNB_PASSWORD_FILE must be set together")
	}
	at := getenv("FETCH_AT", "06:00")
	t, err := time.Parse("15:04", at)
	if err != nil {
		return fmt.Errorf("config: FETCH_AT must be HH:MM, got %q", at)
	}
	f.Hour, f.Minute = t.Hour(), t.Minute()
	return nil
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

// Command budget is a household budgeting app: FNB transactions in, categorised
// spending against monthly targets out. See README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// distroless/static carries no zoneinfo, and the schedule (and anything
	// showing a South African date) needs Africa/Johannesburg. ~450 KB.
	_ "time/tzdata"

	"github.com/17xande-dev/budget/internal/auth"
	"github.com/17xande-dev/budget/internal/budget"
	"github.com/17xande-dev/budget/internal/config"
	"github.com/17xande-dev/budget/internal/db"
	"github.com/17xande-dev/budget/internal/fetch"
	"github.com/17xande-dev/budget/internal/fnb"
	"github.com/17xande-dev/budget/internal/handler"
	"github.com/17xande-dev/budget/internal/ledger"
	"github.com/17xande-dev/budget/internal/mcpserver"
	"github.com/17xande-dev/budget/internal/middleware"
	"golang.org/x/time/rate"
)

// version is reported to MCP clients; set at build time with
// -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	if len(os.Args) > 1 {
		if err := runCommand(cfg, os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := serve(cfg); err != nil {
		log.Fatal(err)
	}
}

// runCommand runs an operator subcommand. These exist as subcommands of the one
// binary, rather than separate cmd/ programs, because the runtime image is
// distroless: `docker compose exec budget /budget reset-password ...` works there
// with no shell and no sqlite3.
func runCommand(cfg config.Config, args []string) error {
	switch args[0] {
	case "reset-password":
		if len(args) != 2 {
			return errors.New("usage: budget reset-password EMAIL  (new password on stdin)")
		}
		d, err := db.Open(cfg.DBPath)
		if err != nil {
			return err
		}
		defer d.Close()
		return resetPassword(auth.NewStore(d), args[1], os.Stdin, os.Stdout)
	default:
		return fmt.Errorf("unknown command %q (known: reset-password)", args[0])
	}
}

func serve(cfg config.Config) error {
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	users := auth.NewStore(database)
	if err := ensureSetupToken(users, cfg.SetupToken, log.Default()); err != nil {
		return fmt.Errorf("setup token: %w", err)
	}

	led, bud := ledger.NewStore(database), budget.NewStore(database)
	fetcher := fetch.New(database, fnb.Chrome{URL: cfg.FNB.ChromeURL}, led, bud, fetch.Config{
		Creds:    fnb.Credentials{Username: cfg.FNB.Username, Password: cfg.FNB.Password, Profile: cfg.FNB.Profile},
		Products: cfg.FNB.Products,
		Hour:     cfg.FNB.Hour,
		Minute:   cfg.FNB.Minute,
	})
	h, err := handler.New(users, led, bud, handler.Config{
		Fetcher:      fetcher,
		BackupDir:    backupDir(cfg),
		CookieSecure: cfg.CookieSecure,
		SessionTTL:   cfg.SessionTTL,
	})
	if err != nil {
		return fmt.Errorf("init handler: %w", err)
	}

	// One per-IP ceiling for every route that verifies a secret. Generous enough
	// that a fumbled password or two never trips it; the per-email tier inside
	// the login handler is the tight one.
	trusted := middleware.ParseTrustedProxies(cfg.TrustedProxies)
	secretLimit := middleware.NewLimiter(rate.Every(10*time.Second), 10).ByIP(trusted)

	mcp := mcpserver.New(mcpserver.Deps{Users: users, Ledger: led, Budget: bud, Trusted: trusted, Version: version})

	app := h.Server(middleware.RequireUser(users), secretLimit, mcp.Handler())

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Expired sessions are already refused on read; this only keeps the table
	// from growing for ever.
	go sweepSessions(ctx, users, time.Hour)
	go fetcher.Schedule(ctx)
	if cfg.BackupKeep > 0 {
		go backupDaily(ctx, database, cfg.BackupDir, cfg.BackupKeep, time.Hour)
	} else {
		log.Print("backups are off (BACKUP_KEEP=0)")
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("listening on :%s", cfg.Port)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// backupDir is where the settings page looks for backups: none when they are off.
func backupDir(cfg config.Config) string {
	if cfg.BackupKeep == 0 {
		return ""
	}
	return cfg.BackupDir
}

func sweepSessions(ctx context.Context, store *auth.Store, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if n, err := store.DeleteExpiredSessions(now); err != nil {
				log.Printf("sweep sessions: %v", err)
			} else if n > 0 {
				log.Printf("swept %d expired session(s)", n)
			}
		}
	}
}

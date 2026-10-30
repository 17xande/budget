package fetch_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/budget/internal/budget"
	"github.com/17xande-dev/budget/internal/db"
	"github.com/17xande-dev/budget/internal/fetch"
	"github.com/17xande-dev/budget/internal/fnb"
	"github.com/17xande-dev/budget/internal/fnb/fnbtest"
	"github.com/17xande-dev/budget/internal/ledger"
)

type env struct {
	t      *testing.T
	db     *sql.DB
	ledger *ledger.Store
	budget *budget.Store
	srv    *fnbtest.Server
	login  *fnbtest.Login
	f      *fetch.Fetcher
}

func fixture(t *testing.T, name string) []byte {
	b, err := os.ReadFile("../importer/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &env{t: t, db: d, ledger: ledger.NewStore(d), budget: budget.NewStore(d)}
	e.srv = fnbtest.New(t,
		fnbtest.Account{Ref: "-1", Product: "DDA", OFX: fixture(t, "cheque.ofx")},
		fnbtest.Account{Ref: "-2", Product: "ZFN", OFX: fixture(t, "card.ofx")},
		fnbtest.Account{Ref: "-3", Product: "MLS", OFX: fixture(t, "cheque.ofx")}, // a loan: not fetched
		fnbtest.Account{Ref: "-4", Product: "DDA"},                                // empty window
	)
	e.login = &fnbtest.Login{Srv: e.srv}
	e.f = fetch.New(d, e.login, e.ledger, e.budget, fetch.Config{
		Creds:      fnb.Credentials{Username: "u", Password: "p"},
		Products:   []string{"DDA", "ZFN"},
		Hour:       6,
		BankingURL: e.srv.Base(),
	})
	return e
}

func TestRunImportsCategorisesAndLogsOff(t *testing.T) {
	e := newEnv(t)
	cat, _ := e.budget.CreateCategory("Groceries", budget.KindExpense, budget.Actor{Via: budget.ViaWeb})
	e.budget.SaveRule(0, "checkers", cat.ID, 0, budget.Actor{Via: budget.ViaWeb})

	run, err := e.f.Run(context.Background(), "schedule", 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "ok" || run.Accounts != 2 || run.Inserted != 12 {
		t.Fatalf("run: %+v", run)
	}
	if !e.srv.LoggedOff {
		t.Error("did not log off")
	}
	// The loan account (-3) was never opened; the empty one (-4) was.
	if !slices.Equal(e.srv.Opened, []string{"-1", "-2", "-4"}) {
		t.Errorf("opened %v, want the two DDA accounts and the card", e.srv.Opened)
	}
	tot, _ := e.ledger.Totals(ledger.Filter{CategoryID: cat.ID})
	if tot.Count != 2 {
		t.Errorf("rules categorised %d fetched rows, want the 2 Checkers rows", tot.Count)
	}
	ims, _ := e.ledger.Imports(10)
	if ims[0].Source != ledger.SourceFNB {
		t.Errorf("import source %q", ims[0].Source)
	}
}

// The daily guard: a scheduled run happens at most once a day, even if the
// server restarts and catches up.
func TestScheduledRunOncePerDay(t *testing.T) {
	e := newEnv(t)
	if _, err := e.f.Run(context.Background(), "schedule", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Run(context.Background(), "schedule", 0); !errors.Is(err, fetch.ErrAlreadyToday) {
		t.Fatalf("second scheduled run: %v", err)
	}
	if e.login.Calls != 1 {
		t.Errorf("logged in %d times", e.login.Calls)
	}
}

// A failed login is recorded and not retried: repeated failed logins are how a
// bank account gets locked.
func TestFailedLoginIsRecordedNotRetried(t *testing.T) {
	e := newEnv(t)
	e.login.Err = errors.New("fnb: could not log in: still at https://www.fnb.co.za/, page begins \"Register\"")
	run, err := e.f.Run(context.Background(), "schedule", 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !strings.Contains(run.Message, "could not log in") {
		t.Fatalf("run: %+v", run)
	}
	if _, err := e.f.Run(context.Background(), "schedule", 0); !errors.Is(err, fetch.ErrAlreadyToday) {
		t.Errorf("a failed scheduled run was retried the same day: %v", err)
	}
	if e.login.Calls != 1 {
		t.Errorf("%d login attempts after a failure", e.login.Calls)
	}
}

func TestManualCooldown(t *testing.T) {
	e := newEnv(t)
	if _, err := e.f.Run(context.Background(), "manual", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Run(context.Background(), "manual", 0); !errors.Is(err, fetch.ErrTooSoon) {
		t.Fatalf("second manual run within the cooldown: %v", err)
	}
}

// An account switched off is downloaded but never imported.
func TestFetchFlagHonoured(t *testing.T) {
	e := newEnv(t)
	e.f.Run(context.Background(), "manual", 0)
	accts, _ := e.ledger.Accounts()
	for _, a := range accts {
		if a.Kind == "credit" {
			e.ledger.SetFetch(a.ID, false)
		}
	}
	e.db.Exec(`DELETE FROM fetch_runs`) // lift the cooldown
	e.db.Exec(`DELETE FROM transactions WHERE account_id IN (SELECT id FROM accounts WHERE kind = 'credit')`)
	run, _ := e.f.Run(context.Background(), "manual", 0)
	if run.Accounts != 1 || run.Inserted != 0 {
		t.Errorf("run with the card switched off: %+v", run)
	}
	tot, _ := e.ledger.Totals(ledger.Filter{})
	if tot.Count != 6 {
		t.Errorf("%d transactions, want only the cheque's 6", tot.Count)
	}
}

// A "running" row left by a crash does not block every run after it.
func TestRecoverInterrupted(t *testing.T) {
	e := newEnv(t)
	e.db.Exec(`INSERT INTO fetch_runs (trigger, started_at, status) VALUES ('manual', ?, 'running')`,
		time.Now().UTC().Format(time.RFC3339))
	if _, err := e.f.Run(context.Background(), "schedule", 0); !errors.Is(err, fetch.ErrBusy) {
		t.Fatalf("with a run in progress: %v", err)
	}
	e.f.RecoverInterrupted()
	runs, _ := e.f.Runs(5)
	if runs[0].Status != "failed" || !strings.Contains(runs[0].Message, "interrupted") {
		t.Fatalf("interrupted run: %+v", runs[0])
	}
	if _, err := e.f.Run(context.Background(), "schedule", 0); err != nil {
		t.Errorf("after recovery: %v", err)
	}
}

func TestNotConfigured(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "n.db"))
	defer d.Close()
	f := fetch.New(d, &fnbtest.Login{}, ledger.NewStore(d), budget.NewStore(d), fetch.Config{})
	if _, err := f.Run(context.Background(), "manual", 0); !errors.Is(err, fetch.ErrNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

func TestNextRun(t *testing.T) {
	e := newEnv(t)
	sast, _ := time.LoadLocation("Africa/Johannesburg")
	before := time.Date(2026, 10, 5, 5, 0, 0, 0, sast)
	if got := e.f.NextRun(before); !got.Equal(time.Date(2026, 10, 5, 6, 0, 0, 0, sast)) {
		t.Errorf("before 06:00: %v", got)
	}
	after := time.Date(2026, 10, 5, 6, 0, 0, 0, sast)
	if got := e.f.NextRun(after); !got.Equal(time.Date(2026, 10, 6, 6, 0, 0, 0, sast)) {
		t.Errorf("at 06:00: %v", got)
	}
}

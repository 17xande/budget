// Package fetch runs the daily FNB fetch: log in, download each account's recent
// transactions, import them, categorise them by rule, log off -- and record every
// attempt, so the status page can say what happened and the guards can stop a run
// repeating.
//
// It fails loudly and never retries in a loop: repeated failed logins are how a
// bank account gets locked, and a fetch that silently stops is worse than one
// that says it failed. A scheduled run happens at most once a day; "Fetch now"
// is refused while a run is going or within minutes of the last one.
package fetch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/budget"
	"github.com/17xande-dev/budget/internal/fnb"
	"github.com/17xande-dev/budget/internal/importer"
	"github.com/17xande-dev/budget/internal/ledger"
)

type Config struct {
	Creds fnb.Credentials
	// Products are the FNB product codes fetched (DDA cheque, ZFN credit card
	// by default); a loan or notice account is usually not spending.
	Products []string
	// At is the daily time, in South Africa, as hour and minute.
	Hour, Minute int
	// WindowDays is how far back each fetch reaches. FNB's quick search serves
	// 60 days; fetching all of it every day lets a missed day heal itself,
	// since an import never duplicates.
	WindowDays int
	BankingURL string
}

// Configured reports whether there are credentials to fetch with at all.
func (c Config) Configured() bool { return c.Creds.Username != "" && c.Creds.Password != "" }

var sast = func() *time.Location {
	l, err := time.LoadLocation("Africa/Johannesburg")
	if err != nil {
		return time.FixedZone("SAST", 2*60*60)
	}
	return l
}()

type Fetcher struct {
	db     *sql.DB
	login  fnb.Login
	ledger *ledger.Store
	budget *budget.Store
	cfg    Config
	now    func() time.Time
}

func New(db *sql.DB, login fnb.Login, led *ledger.Store, bud *budget.Store, cfg Config) *Fetcher {
	if cfg.WindowDays == 0 {
		cfg.WindowDays = 60
	}
	if cfg.BankingURL == "" {
		cfg.BankingURL = fnb.BankingURL
	}
	return &Fetcher{db: db, login: login, ledger: led, budget: bud, cfg: cfg, now: time.Now}
}

func (f *Fetcher) Configured() bool { return f.cfg.Configured() }

// Run is one attempt, as the status page shows it.
type Run struct {
	ID         int64
	Trigger    string
	UserName   string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     string
	Accounts   int
	Inserted   int
	Message    string
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Runs returns recent attempts, newest first.
func (f *Fetcher) Runs(limit int) ([]Run, error) {
	rows, err := f.db.Query(`SELECT r.id, r.trigger, COALESCE(u.name, ''), r.started_at, r.finished_at, r.status,
		r.accounts, r.inserted, r.message
		FROM fetch_runs r LEFT JOIN users u ON u.id = r.user_id ORDER BY r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var started, finished string
		if err := rows.Scan(&r.ID, &r.Trigger, &r.UserName, &started, &finished, &r.Status, &r.Accounts, &r.Inserted, &r.Message); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339, started)
		r.FinishedAt, _ = time.Parse(time.RFC3339, finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Last returns the most recent attempt, or nil.
func (f *Fetcher) Last() (*Run, error) {
	runs, err := f.Runs(1)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}

// RecoverInterrupted closes runs a restart cut off, so a "running" row from a
// previous process does not block every run after it.
func (f *Fetcher) RecoverInterrupted() error {
	_, err := f.db.Exec(`UPDATE fetch_runs SET status = 'failed', finished_at = ?,
		message = 'interrupted: the server stopped while this run was going' WHERE status = 'running'`, formatTime(f.now()))
	return err
}

var (
	ErrNotConfigured = errors.New("the FNB fetch is not configured: set FNB_USERNAME and FNB_PASSWORD_FILE")
	ErrBusy          = errors.New("a fetch is already running")
	ErrTooSoon       = errors.New("a fetch ran in the last 10 minutes; wait before trying again")
	ErrAlreadyToday  = errors.New("the scheduled fetch has already run today")
)

// manualCooldown is the least time between a manual run and the run before it:
// enough that a double click, or an impatient second one, cannot log in twice.
const manualCooldown = 10 * time.Minute

// start records a new run if the guards allow it. Each guard is part of the one
// INSERT, so two triggers at the same moment cannot both start.
func (f *Fetcher) start(trigger string, userID int64) (int64, error) {
	if !f.cfg.Configured() {
		return 0, ErrNotConfigured
	}
	now := f.now()
	var uid any
	if userID != 0 {
		uid = userID
	}
	// A "running" row older than the run timeout is a crash nobody recovered;
	// it does not block forever.
	staleBefore := formatTime(now.Add(-runTimeout - time.Minute))
	var cond string
	var args []any
	switch trigger {
	case "manual":
		cond = `NOT EXISTS (SELECT 1 FROM fetch_runs WHERE (status = 'running' AND started_at > ?) OR started_at > ?)`
		args = []any{staleBefore, formatTime(now.Add(-manualCooldown))}
	case "schedule":
		y, m, d := now.In(sast).Date()
		midnight := time.Date(y, m, d, 0, 0, 0, 0, sast)
		cond = `NOT EXISTS (SELECT 1 FROM fetch_runs WHERE (status = 'running' AND started_at > ?) OR (trigger = 'schedule' AND started_at >= ?))`
		args = []any{staleBefore, formatTime(midnight)}
	default:
		return 0, fmt.Errorf("unknown trigger %q", trigger)
	}
	res, err := f.db.Exec(`INSERT INTO fetch_runs (trigger, user_id, started_at, status)
		SELECT ?, ?, ?, 'running' WHERE `+cond, append([]any{trigger, uid, formatTime(now)}, args...)...)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		id, _ := res.LastInsertId()
		return id, nil
	}
	// Refused: say which guard, for the person pressing the button.
	var running int
	f.db.QueryRow(`SELECT count(*) FROM fetch_runs WHERE status = 'running' AND started_at > ?`, staleBefore).Scan(&running)
	switch {
	case running > 0:
		return 0, ErrBusy
	case trigger == "schedule":
		return 0, ErrAlreadyToday
	default:
		return 0, ErrTooSoon
	}
}

func (f *Fetcher) finish(id int64, status string, accounts, inserted int, msg string) {
	if _, err := f.db.Exec(`UPDATE fetch_runs SET status = ?, finished_at = ?, accounts = ?, inserted = ?, message = ? WHERE id = ?`,
		status, formatTime(f.now()), accounts, inserted, msg, id); err != nil {
		log.Printf("fetch: record run %d: %v", id, err)
	}
}

// runTimeout bounds one whole run: a login is ~15s and each account a second or
// two, so five minutes is a hung browser or bank, not a slow one.
const runTimeout = 5 * time.Minute

// Run starts and executes a run synchronously.
func (f *Fetcher) Run(ctx context.Context, trigger string, userID int64) (Run, error) {
	id, err := f.start(trigger, userID)
	if err != nil {
		return Run{}, err
	}
	f.execute(ctx, id)
	runs, err := f.Runs(1)
	if err != nil || len(runs) == 0 {
		return Run{}, err
	}
	return runs[0], nil
}

// RunNow starts a manual run and executes it in the background, so the request
// that pressed the button returns at once. The guards are checked before it
// returns, so a refusal is reported to the person, not just logged.
func (f *Fetcher) RunNow(userID int64) error {
	id, err := f.start("manual", userID)
	if err != nil {
		return err
	}
	go f.execute(context.Background(), id)
	return nil
}

func (f *Fetcher) execute(parent context.Context, id int64) {
	ctx, cancel := context.WithTimeout(parent, runTimeout)
	defer cancel()
	accounts, inserted, problems, err := f.fetch(ctx)
	switch {
	case err != nil:
		log.Printf("fetch: run %d failed: %v", id, err)
		f.finish(id, "failed", accounts, inserted, err.Error())
	case len(problems) > 0:
		msg := strings.Join(problems, "; ")
		log.Printf("fetch: run %d partly failed: %s", id, msg)
		f.finish(id, "failed", accounts, inserted, msg)
	default:
		log.Printf("fetch: run %d: %d accounts, %d new transactions", id, accounts, inserted)
		f.finish(id, "ok", accounts, inserted, "")
	}
}

// fetch is one run's work. A failure to log in or list accounts fails the run; a
// failure on one account is recorded and the others still import.
func (f *Fetcher) fetch(ctx context.Context) (accounts, inserted int, problems []string, err error) {
	res, err := f.login.Login(ctx, f.cfg.Creds)
	if err != nil {
		return 0, 0, nil, err
	}
	sess, err := fnb.NewSession(res.Cookies, res.UserAgent, f.cfg.BankingURL)
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() {
		// Always log off, on a context of its own: the run's may have expired.
		lctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sess.Logoff(lctx); err != nil {
			log.Printf("fetch: log off: %v", err)
		}
	}()

	accts, err := sess.Accounts(ctx)
	if err != nil {
		return 0, 0, nil, err
	}
	to := f.now().In(sast)
	from := to.AddDate(0, 0, -f.cfg.WindowDays)
	stamp := to.Format("2006-01-02")
	for _, a := range accts {
		if !slices.Contains(f.cfg.Products, a.Product) {
			continue
		}
		data, err := sess.Export(ctx, a, from, to)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s account: %v", a.Product, err))
			continue
		}
		if data == nil {
			continue // nothing in the window
		}
		name := "FNB " + a.Product + " " + stamp
		sts, err := importer.Parse(importer.File{Name: name, Data: data})
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s account: %v", a.Product, err))
			continue
		}
		for _, st := range sts {
			on, err := f.ledger.FetchEnabled(st.AccountRef)
			if err != nil {
				return accounts, inserted, problems, err
			}
			if !on {
				continue
			}
			im, err := f.ledger.Import(st, ledger.SourceFNB, name, 0, f.now())
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s account: import: %v", a.Product, err))
				continue
			}
			accounts++
			inserted += im.Inserted
			if _, err := f.budget.ApplyRules(budget.Scope{ImportID: im.ID}); err != nil {
				problems = append(problems, fmt.Sprintf("%s account: rules: %v", a.Product, err))
			}
		}
	}
	return accounts, inserted, problems, nil
}

// NextRun is when the schedule next fires after now.
func (f *Fetcher) NextRun(now time.Time) time.Time {
	local := now.In(sast)
	next := time.Date(local.Year(), local.Month(), local.Day(), f.cfg.Hour, f.cfg.Minute, 0, 0, sast)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Schedule runs the daily fetch until ctx ends. On start it catches up: if
// today's time has passed and today's scheduled run has not happened (the server
// was down at the time), it runs now -- the daily guard keeps that to once.
func (f *Fetcher) Schedule(ctx context.Context) {
	if !f.cfg.Configured() {
		log.Print("fetch: FNB_USERNAME / FNB_PASSWORD_FILE not set; the daily fetch is off")
		return
	}
	if err := f.RecoverInterrupted(); err != nil {
		log.Printf("fetch: recover interrupted runs: %v", err)
	}
	now := f.now()
	local := now.In(sast)
	if today := time.Date(local.Year(), local.Month(), local.Day(), f.cfg.Hour, f.cfg.Minute, 0, 0, sast); !local.Before(today) {
		f.scheduled(ctx)
	}
	for {
		next := f.NextRun(f.now())
		log.Printf("fetch: next run %s", next.Format("Mon 2 Jan 15:04 MST"))
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			f.scheduled(ctx)
		}
	}
}

func (f *Fetcher) scheduled(ctx context.Context) {
	if _, err := f.Run(ctx, "schedule", 0); err != nil && !errors.Is(err, ErrAlreadyToday) {
		log.Printf("fetch: scheduled run not started: %v", err)
	}
}

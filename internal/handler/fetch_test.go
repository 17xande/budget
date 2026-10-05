package handler_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/budget/internal/auth"
	"github.com/17xande-dev/budget/internal/budget"
	"github.com/17xande-dev/budget/internal/db"
	"github.com/17xande-dev/budget/internal/fetch"
	"github.com/17xande-dev/budget/internal/fnb"
	"github.com/17xande-dev/budget/internal/fnb/fnbtest"
	"github.com/17xande-dev/budget/internal/handler"
	"github.com/17xande-dev/budget/internal/ledger"
	"github.com/17xande-dev/budget/internal/middleware"
	"golang.org/x/time/rate"
)

// fetchEnv is newEnv with a fetcher wired to a fake FNB.
func fetchEnv(t *testing.T, loginErr error) (*env, *fetch.Fetcher) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "fh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	users := auth.NewStore(d)
	users.CreateSetupToken(setupToken, time.Now())
	led, bud := ledger.NewStore(d), budget.NewStore(d)

	ofx, _ := os.ReadFile("../importer/testdata/cheque.ofx")
	bank := fnbtest.New(t, fnbtest.Account{Ref: "-1", Product: "DDA", OFX: ofx})
	f := fetch.New(d, &fnbtest.Login{Srv: bank, Err: loginErr}, led, bud, fetch.Config{
		Creds: fnb.Credentials{Username: "u", Password: "p"}, Products: []string{"DDA"}, Hour: 6, BankingURL: bank.Base(),
	})
	h, err := handler.New(users, led, bud, handler.Config{SessionTTL: time.Hour, Fetcher: f})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Server(middleware.RequireUser(users), middleware.NewLimiter(rate.Inf, 1).ByIP(nil), nil))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, users: users, h: h}, f
}

func waitForRun(t *testing.T, f *fetch.Fetcher) fetch.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if last, _ := f.Last(); last != nil && last.Status != "running" {
			return *last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the fetch never finished")
	return fetch.Run{}
}

func TestFetchNow(t *testing.T) {
	e, f := fetchEnv(t, nil)
	owner := e.claim()

	if page := owner.get("/fetch").body; !strings.Contains(page, "Next scheduled run") {
		t.Fatal("status page does not show the schedule")
	}
	r := owner.post("/fetch", "/fetch", nil)
	if r.code != http.StatusSeeOther || r.loc != "/fetch?notice=fetch_started" {
		t.Fatalf("fetch now: %d %q", r.code, r.loc)
	}
	run := waitForRun(t, f)
	if run.Status != "ok" || run.Inserted != 6 || run.Trigger != "manual" {
		t.Fatalf("run: %+v", run)
	}
	if page := owner.get("/fetch").body; !strings.Contains(page, "6 new transactions") || !strings.Contains(page, "Fetch now (Owner)") {
		t.Error("status page does not show the run")
	}
	// Pressing again within the cooldown is refused, with the reason.
	if r := owner.post("/fetch", "/fetch", nil); r.code != http.StatusConflict || !strings.Contains(r.body, "last 10 minutes") {
		t.Errorf("second fetch: %d", r.code)
	}
}

// A failed fetch must be impossible to miss: a banner on the overview.
func TestFailedFetchBanner(t *testing.T) {
	e, f := fetchEnv(t, errors.New("fnb: could not log in: still at https://www.fnb.co.za/"))
	owner := e.claim()
	owner.post("/fetch", "/fetch", nil)
	if run := waitForRun(t, f); run.Status != "failed" {
		t.Fatalf("run: %+v", run)
	}
	home := owner.get("/").body
	if !strings.Contains(home, "The last bank fetch failed") || !strings.Contains(home, "could not log in") {
		t.Error("no failure banner on the overview")
	}
}

func TestNoFetcherConfigured(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	if page := owner.get("/fetch").body; !strings.Contains(page, "not set up") {
		t.Error("an unconfigured fetch does not say so")
	}
	if r := owner.post("/fetch", "/fetch", nil); r.code != http.StatusNotFound {
		t.Errorf("fetch now without a fetcher: %d", r.code)
	}
}

func TestAccountFetchToggleIsStrict(t *testing.T) {
	_, owner := seeded(t)
	page := owner.get("/accounts").body
	if !strings.Contains(page, "Stop fetching this account") {
		t.Fatal("no fetch toggle")
	}
	for _, v := range []url.Values{{}, {"fetch": {"yes"}}} {
		if r := owner.post("/accounts", "/accounts/1/fetch", v); r.code != http.StatusBadRequest {
			t.Errorf("fetch=%v: %d", v, r.code)
		}
	}
	if r := owner.post("/accounts", "/accounts/1/fetch", url.Values{"fetch": {"0"}}); r.code != http.StatusSeeOther {
		t.Fatalf("switch off: %d", r.code)
	}
	if !strings.Contains(owner.get("/accounts").body, "not fetched") {
		t.Error("switching off did not stick")
	}
}

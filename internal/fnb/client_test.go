package fnb_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/17xande-dev/budget/internal/fnb"
	"github.com/17xande-dev/budget/internal/fnb/fnbtest"
)

func ofx(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../importer/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExportFlow(t *testing.T) {
	srv := fnbtest.New(t,
		fnbtest.Account{Ref: "-7148417", Product: "DDA", OFX: ofx(t, "cheque.ofx")},
		fnbtest.Account{Ref: "-20427857", Product: "ZFN", OFX: ofx(t, "card.ofx")},
		fnbtest.Account{Ref: "-1", Product: "DDA"}, // nothing in the window
	)
	s, err := fnb.NewSession(srv.Cookies(), "test-agent", srv.Base())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	accts, err := s.Accounts(ctx)
	if err != nil || len(accts) != 3 || accts[1] != (fnb.Account{Ref: "-20427857", Product: "ZFN"}) {
		t.Fatalf("accounts: %+v %v", accts, err)
	}
	to := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	zip, err := s.Export(ctx, accts[0], to.AddDate(0, 0, -60), to)
	if err != nil || len(zip) == 0 || string(zip[:2]) != "PK" {
		t.Fatalf("export: %d bytes %v", len(zip), err)
	}
	// FNB's "nothing in the window" is an empty 200: no data, no error.
	empty, err := s.Export(ctx, accts[2], to.AddDate(0, 0, -60), to)
	if err != nil || empty != nil {
		t.Fatalf("empty export: %v %v", empty, err)
	}
	if err := s.Logoff(ctx); err != nil || !srv.LoggedOff {
		t.Fatalf("logoff: %v %v", err, srv.LoggedOff)
	}
	// The flow is the recorded one, step for step.
	want := []string{
		"accounts.summaryofaccountbalances.navigator.SummaryOfAccountBalances",
		"transactionhistory.navigator.TransactionHistoryRedirect",
		"transactionhistory.navigator.TransactionHistoryRedirect",
		"accounts.transactionhistory.navigator.TransactionHistoryDownloadEzi",
		"accounts.transactionhistory.navigator.TransactionHistoryDDADownload",
	}
	if !slices.Equal(srv.Calls[:5], want) {
		t.Errorf("flow:\n got %q\nwant %q", srv.Calls[:5], want)
	}
}

// A session that is not really logged in gets FNB's empty 200s; that must be an
// error, not "you have no accounts".
func TestNotLoggedIn(t *testing.T) {
	srv := fnbtest.New(t, fnbtest.Account{Ref: "-1", Product: "DDA"})
	srv.Token = "something-else"
	s, _ := fnb.NewSession(nil, "test-agent", srv.Base())
	if _, err := s.Accounts(context.Background()); !errors.Is(err, fnb.ErrNoAccounts) {
		t.Fatalf("got %v, want ErrNoAccounts", err)
	}
}

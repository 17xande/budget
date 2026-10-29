package budget_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/17xande/budget/internal/budget"
	"github.com/17xande/budget/internal/db"
	"github.com/17xande/budget/internal/importer"
	"github.com/17xande/budget/internal/ledger"
)

var web = budget.Actor{Via: budget.ViaWeb}

type env struct {
	t   *testing.T
	db  *sql.DB
	b   *budget.Store
	l   *ledger.Store
	acc int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return &env{t: t, db: d, b: budget.NewStore(d), l: ledger.NewStore(d)}
}

// add imports transactions into one account (ref) and returns the import id.
func (e *env) add(ref string, txns ...importer.Txn) int64 {
	e.t.Helper()
	im, err := e.l.Import(importer.Statement{AccountRef: ref, AccountLabel: "Cheque Acc", Txns: txns},
		ledger.SourceUpload, "t", 0, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return im.ID
}

func tx(date string, cents int64, desc string) importer.Txn {
	return importer.Txn{Date: date, AmountCents: cents, Description: desc}
}

func (e *env) cat(name, kind string) int64 {
	e.t.Helper()
	c, err := e.b.CreateCategory(name, kind, web)
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

// categoryOf returns a transaction's category id (0 for none) and source.
func (e *env) categoryOf(desc string) (int64, string) {
	e.t.Helper()
	var id sql.NullInt64
	var src string
	if err := e.db.QueryRow(`SELECT category_id, category_source FROM transactions WHERE description = ?`, desc).Scan(&id, &src); err != nil {
		e.t.Fatal(err)
	}
	return id.Int64, src
}

func (e *env) txnID(desc string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM transactions WHERE description = ?`, desc).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestCategoryNamesAreUniqueIgnoringCase(t *testing.T) {
	e := newEnv(t)
	e.cat("Groceries", budget.KindExpense)
	if _, err := e.b.CreateCategory("  groceries ", budget.KindExpense, web); !errors.Is(err, budget.ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	other := e.cat("Fuel", budget.KindExpense)
	if err := e.b.UpdateCategory(other, "GROCERIES", budget.KindExpense, web); !errors.Is(err, budget.ErrNameTaken) {
		t.Fatalf("rename onto another's name: %v", err)
	}
	// Changing only the case of your own name is allowed.
	if err := e.b.UpdateCategory(other, "FUEL", budget.KindExpense, web); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	if _, err := e.b.CreateCategory("x", "spending", web); !errors.Is(err, budget.ErrInvalid) {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestSeedStarterIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.cat("groceries", budget.KindExpense) // one already present, differently cased
	n, err := e.b.SeedStarter(web)
	if err != nil || n != len(budget.StarterCategories)-1 {
		t.Fatalf("first seed added %d (%v)", n, err)
	}
	if n, _ := e.b.SeedStarter(web); n != 0 {
		t.Errorf("second seed added %d", n)
	}
}

func TestRulesLongestPatternWinsAndNeverOverride(t *testing.T) {
	e := newEnv(t)
	food := e.cat("Groceries", budget.KindExpense)
	shop := e.cat("Shopping", budget.KindExpense)
	e.add("1",
		tx("2026-10-01", -10000, "WOOLWORTHS FOOD SANDTON"),
		tx("2026-10-02", -20000, "WOOLWORTHS SANDTON"),
		tx("2026-10-03", -3000, "WOOLWORTHS 50% SALE"),
	)
	// A choice made by hand stands, whatever the rules say.
	if _, err := e.b.Categorise(e.txnID("WOOLWORTHS SANDTON"), food, web); err != nil {
		t.Fatal(err)
	}
	e.b.SaveRule(0, "woolworths", shop, 0, web)
	e.b.SaveRule(0, "Woolworths   food", food, 0, web) // whitespace collapsed like descriptions
	e.b.SaveRule(0, "50%", food, 0, web)               // % is literal, not a wildcard

	n, err := e.b.ApplyRules(budget.Scope{})
	if err != nil || n != 2 {
		t.Fatalf("applied %d (%v), want 2", n, err)
	}
	if c, src := e.categoryOf("WOOLWORTHS FOOD SANDTON"); c != food || src != "rule" {
		t.Errorf("food line: category %d source %q, want groceries by rule", c, src)
	}
	if c, src := e.categoryOf("WOOLWORTHS SANDTON"); c != food || src != "manual" {
		t.Errorf("manual choice overridden: %d %q", c, src)
	}
	// "50%" (3 chars) loses to "woolworths" (10): longest wins, and % is literal.
	if c, _ := e.categoryOf("WOOLWORTHS 50% SALE"); c != shop {
		t.Errorf("50%% sale: %d, want shopping", c)
	}
}

func TestRuleScopedToAccountAndImport(t *testing.T) {
	e := newEnv(t)
	fees := e.cat("Bank fees", budget.KindExpense)
	e.add("1", tx("2026-10-01", -500, "FEE A"))
	imp := e.add("2", tx("2026-10-01", -600, "FEE B"))
	var acct1 int64
	e.db.QueryRow(`SELECT id FROM accounts WHERE ref = '1'`).Scan(&acct1)
	e.b.SaveRule(0, "FEE", fees, acct1, web)

	// Scoped to import 2: the rule is for account 1, so nothing.
	if n, _ := e.b.ApplyRules(budget.Scope{ImportID: imp}); n != 0 {
		t.Errorf("rule for account 1 touched account 2's import: %d", n)
	}
	if n, _ := e.b.ApplyRules(budget.Scope{}); n != 1 {
		t.Errorf("applied %d, want 1", n)
	}
	if c, _ := e.categoryOf("FEE B"); c != 0 {
		t.Error("account-scoped rule categorised another account")
	}
}

func TestCategoriseAuditsAndRefusesArchived(t *testing.T) {
	e := newEnv(t)
	a := e.cat("A", budget.KindExpense)
	b := e.cat("B", budget.KindExpense)
	e.add("1", tx("2026-10-01", -100, "X"))
	id := e.txnID("X")

	ai := budget.Actor{Via: budget.ViaMCP, TokenName: "laptop"}
	if changed, err := e.b.Categorise(id, a, ai); !changed || err != nil {
		t.Fatalf("categorise: %v %v", changed, err)
	}
	if changed, _ := e.b.Categorise(id, a, ai); changed {
		t.Error("a no-op reported a change")
	}
	if _, src := e.categoryOf("X"); src != "ai" {
		t.Errorf("source %q, want ai", src)
	}
	e.b.SetArchived(b, true, web)
	if _, err := e.b.Categorise(id, b, web); !errors.Is(err, budget.ErrArchived) {
		t.Errorf("into archived: %v", err)
	}
	if _, err := e.b.Categorise(id, 9999, web); !errors.Is(err, budget.ErrNotFound) {
		t.Errorf("into nonexistent: %v", err)
	}

	var before, after, via, token string
	e.db.QueryRow(`SELECT before, after, via, token_name FROM audit_log WHERE action = 'categorise' AND subject_id = ?`, id).
		Scan(&before, &after, &via, &token)
	if before != "" || after == "" || via != "mcp" || token != "laptop" {
		t.Errorf("audit: before %q after %q via %q token %q", before, after, via, token)
	}
}

// A batch is all or nothing: one bad id must not leave half the batch applied.
func TestCategoriseManyIsAtomic(t *testing.T) {
	e := newEnv(t)
	a := e.cat("A", budget.KindExpense)
	e.add("1", tx("2026-10-01", -100, "X"), tx("2026-10-02", -100, "Y"))
	_, err := e.b.CategoriseMany(map[int64]int64{e.txnID("X"): a, 99999: a}, web)
	if err == nil {
		t.Fatal("a batch with an unknown transaction succeeded")
	}
	if c, _ := e.categoryOf("X"); c != 0 {
		t.Error("part of a failed batch was applied")
	}
}

func TestTargets(t *testing.T) {
	e := newEnv(t)
	food := e.cat("Groceries", budget.KindExpense)
	tr := e.cat("Transfers", budget.KindTransfer)
	if err := e.b.SetTarget("2026-09", food, 500000, web); err != nil {
		t.Fatal(err)
	}
	if err := e.b.SetTarget("2026-09", tr, 100, web); !errors.Is(err, budget.ErrInvalid) {
		t.Errorf("target on a transfer category: %v", err)
	}
	e.b.SetTarget("2026-10", food, 600000, web)
	// Copy never overwrites what the destination already has.
	if n, _ := e.b.CopyTargets("2026-09", "2026-10", web); n != 0 {
		t.Errorf("copy overwrote %d", n)
	}
	if n, _ := e.b.CopyTargets("2026-09", "2026-11", web); n != 1 {
		t.Errorf("copy into an empty month: %d", n)
	}
	nov, _ := e.b.Targets("2026-11")
	if nov[food] != 500000 {
		t.Errorf("copied target %d", nov[food])
	}
	e.b.SetTarget("2026-11", food, -1, web) // clear
	if nov, _ := e.b.Targets("2026-11"); len(nov) != 0 {
		t.Errorf("cleared target still there: %v", nov)
	}
}

func TestSummary(t *testing.T) {
	e := newEnv(t)
	food := e.cat("Groceries", budget.KindExpense)
	idle := e.cat("Entertainment", budget.KindExpense)
	pay := e.cat("Salary", budget.KindIncome)
	tr := e.cat("Transfers", budget.KindTransfer)
	e.add("1",
		tx("2026-10-01", -100000, "SHOP 1"),
		tx("2026-10-05", -50000, "SHOP 2"),
		tx("2026-10-06", 20000, "SHOP REFUND"), // a refund reduces spending
		tx("2026-10-25", 3000000, "SALARY"),
		tx("2026-10-26", -500000, "TO SAVINGS"),
		tx("2026-10-27", -1234, "MYSTERY"),
		tx("2026-09-30", -999999, "SEPTEMBER SHOP"), // another month: excluded
	)
	for desc, cat := range map[string]int64{"SHOP 1": food, "SHOP 2": food, "SHOP REFUND": food, "SALARY": pay, "TO SAVINGS": tr, "SEPTEMBER SHOP": food} {
		e.b.Categorise(e.txnID(desc), cat, web)
	}
	e.b.SetTarget("2026-10", food, 100000, web)
	e.b.SetTarget("2026-10", idle, 50000, web)

	s, err := e.b.Summary("2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if s.SpentCents != 130000 || s.IncomeCents != 3000000 || s.TransferCents != -500000 {
		t.Errorf("spent %d income %d transfers %d", s.SpentCents, s.IncomeCents, s.TransferCents)
	}
	if s.NetCents() != 3000000-130000 {
		t.Errorf("net %d", s.NetCents())
	}
	if s.UncatCount != 1 || s.UncatOutCents != -1234 || s.UncatInCents != 0 {
		t.Errorf("uncategorised: %d %d %d", s.UncatCount, s.UncatInCents, s.UncatOutCents)
	}
	if s.TargetCents != 150000 {
		t.Errorf("target total %d", s.TargetCents)
	}
	byName := map[string]budget.Line{}
	for _, l := range s.Expenses {
		byName[l.Name] = l
	}
	g := byName["Groceries"]
	if g.SpentCents() != 130000 || !g.Over() || g.RemainingCents() != -30000 || g.Count != 3 {
		t.Errorf("groceries: spent %d over %v remaining %d count %d", g.SpentCents(), g.Over(), g.RemainingCents(), g.Count)
	}
	// A category with a target and nothing spent is still listed.
	if ent, ok := byName["Entertainment"]; !ok || ent.SpentCents() != 0 || ent.Over() {
		t.Errorf("entertainment: %+v (listed %v)", ent, ok)
	}

	// Archived with no transactions this month: gone. With some: still shown.
	e.b.SetArchived(idle, true, web)
	e.b.SetArchived(food, true, web)
	s, _ = e.b.Summary("2026-10")
	names := map[string]bool{}
	for _, l := range s.Expenses {
		names[l.Name] = true
	}
	if names["Entertainment"] || !names["Groceries"] {
		t.Errorf("archived handling: %v", names)
	}
}

func TestSummaryRemainingCents(t *testing.T) {
	exp := func(target int64, hasTarget bool, spent int64) budget.Line {
		return budget.Line{Kind: budget.KindExpense, Target: sql.NullInt64{Int64: target, Valid: hasTarget}, TotalCents: -spent}
	}
	for name, tc := range map[string]struct {
		s    budget.Summary
		want int64
	}{
		"nothing":                  {budget.Summary{}, 0},
		"no targets":               {budget.Summary{Expenses: []budget.Line{exp(0, false, 5000)}, SpentCents: 5000}, 0},
		"under target":             {budget.Summary{Expenses: []budget.Line{exp(10000, true, 4000)}}, 6000},
		"over-spent subtracts":     {budget.Summary{Expenses: []budget.Line{exp(10000, true, 4000), exp(5000, true, 8000)}}, 3000},
		"untargeted spend ignored": {budget.Summary{Expenses: []budget.Line{exp(10000, true, 4000), exp(0, false, 9000)}}, 6000},
		"uncategorised ignored":    {budget.Summary{Expenses: []budget.Line{exp(10000, true, 4000)}, UncatOutCents: -7000, UncatCount: 2}, 6000},
		"refund adds headroom":     {budget.Summary{Expenses: []budget.Line{exp(10000, true, -500)}}, 10500},
	} {
		if got := tc.s.RemainingCents(); got != tc.want {
			t.Errorf("%s: RemainingCents = %d, want %d", name, got, tc.want)
		}
	}
}

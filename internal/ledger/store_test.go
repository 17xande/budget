package ledger_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/17xande/budget/internal/db"
	"github.com/17xande/budget/internal/importer"
	"github.com/17xande/budget/internal/ledger"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *ledger.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return ledger.NewStore(d)
}

// fixture parses one of the importer's synthetic FNB files.
func fixture(t *testing.T, name string) importer.Statement {
	t.Helper()
	data, err := os.ReadFile("../importer/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	sts, err := importer.Parse(importer.File{Name: name, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return sts[0]
}

func importFile(t *testing.T, s *ledger.Store, name string) ledger.Import {
	t.Helper()
	im, err := s.Import(fixture(t, name), ledger.SourceUpload, name, 0, now)
	if err != nil {
		t.Fatalf("import %s: %v", name, err)
	}
	return im
}

// The sequence that happens in real life: the same period arrives as a CSV, again
// as the same CSV, as OFX, and as a wider advanced export. Only what is genuinely
// new may land, and the identical same-day pair must stay two rows.
func TestImportIsIdempotentAcrossFormats(t *testing.T) {
	s := newStore(t)
	steps := []struct {
		file              string
		inserted, skipped int
	}{
		{"cheque.csv", 6, 0},
		{"cheque.csv", 0, 6},
		{"cheque.ofx", 0, 6},
		{"cheque-advanced.ofx", 2, 6}, // two older rows the quick exports did not reach
		{"cheque.ofx", 0, 6},
	}
	for _, st := range steps {
		im := importFile(t, s, st.file)
		if im.Inserted != st.inserted || im.Skipped != st.skipped {
			t.Errorf("%s: inserted %d skipped %d, want %d and %d", st.file, im.Inserted, im.Skipped, st.inserted, st.skipped)
		}
	}
	tot, _ := s.Totals(ledger.Filter{})
	if tot.Count != 8 {
		t.Fatalf("ledger holds %d transactions, want 8", tot.Count)
	}
	pair, _ := s.Transactions(ledger.Filter{Query: "CHECKERS"}, 100)
	if len(pair) != 2 {
		t.Errorf("identical same-day pair stored as %d rows, want 2", len(pair))
	}
}

// The card's OFX gives an identical pair the same FITID and writes "&amp;" where
// the CSV has "&"; neither may produce a duplicate or a loss.
func TestCardCSVThenOFX(t *testing.T) {
	s := newStore(t)
	if im := importFile(t, s, "card.csv"); im.Inserted != 6 {
		t.Fatalf("card.csv inserted %d", im.Inserted)
	}
	if im := importFile(t, s, "card.ofx"); im.Inserted != 0 || im.Skipped != 6 {
		t.Fatalf("card.ofx after card.csv: inserted %d skipped %d", im.Inserted, im.Skipped)
	}
}

func TestAccountsCreatedAndRenamesStick(t *testing.T) {
	s := newStore(t)
	importFile(t, s, "card.ofx") // OFX carries no label: the masked number says "card"
	importFile(t, s, "cheque.csv")
	accts, _ := s.Accounts()
	if len(accts) != 2 {
		t.Fatalf("%d accounts", len(accts))
	}
	byRef := map[string]ledger.Account{}
	for _, a := range accts {
		byRef[a.Ref] = a
	}
	card, cheque := byRef["400000******1234"], byRef["62000000001"]
	if card.Kind != "credit" || card.Name != "Credit card ···1234" {
		t.Errorf("card: %+v", card)
	}
	if cheque.Kind != "cheque" || cheque.Name != "Cheque account ···0001" || cheque.TxnCount != 6 ||
		cheque.FirstDate != "2026-08-14" || cheque.LastDate != "2026-10-02" {
		t.Errorf("cheque: %+v", cheque)
	}

	if ok, err := s.UpdateAccount(cheque.ID, "Joint cheque", "cheque"); !ok || err != nil {
		t.Fatalf("rename: %v %v", ok, err)
	}
	importFile(t, s, "cheque.ofx")
	accts, _ = s.Accounts()
	for _, a := range accts {
		if a.ID == cheque.ID && a.Name != "Joint cheque" {
			t.Errorf("a re-import undid the rename: %q", a.Name)
		}
	}
	if _, err := s.UpdateAccount(cheque.ID, "x", "piggy bank"); err == nil {
		t.Error("an unknown kind was accepted")
	}
}

// A statement that fails part-way must leave nothing: no half-imported file
// whose second upload then reports misleading counts.
func TestImportIsAtomic(t *testing.T) {
	s := newStore(t)
	st := fixture(t, "cheque.csv")
	st.Txns[3].Date = "2026-19-99" // refused by the posted_on CHECK
	if _, err := s.Import(st, ledger.SourceUpload, "bad.csv", 0, now); err == nil {
		t.Fatal("an invalid date was stored")
	}
	tot, _ := s.Totals(ledger.Filter{})
	ims, _ := s.Imports(10)
	if tot.Count != 0 || len(ims) != 0 {
		t.Fatalf("a failed import left %d transactions and %d import rows", tot.Count, len(ims))
	}
}

func TestRecordFailure(t *testing.T) {
	s := newStore(t)
	if err := s.RecordFailure(ledger.SourceUpload, "notes.txt", 0, errors.New("not an FNB export"), now); err != nil {
		t.Fatal(err)
	}
	ims, _ := s.Imports(10)
	if len(ims) != 1 || ims[0].Error != "not an FNB export" || ims[0].AccountName != "" {
		t.Fatalf("got %+v", ims)
	}
}

func TestFiltersAndTotalsAgree(t *testing.T) {
	s := newStore(t)
	importFile(t, s, "cheque-advanced.ofx")
	importFile(t, s, "card.csv")
	accts, _ := s.Accounts()

	filters := []ledger.Filter{
		{},
		{Month: "2026-10"},
		{Month: "2026-09", AccountID: accts[0].ID},
		{Query: "monthly account"},
		{Query: "&"},
		{Query: "50%"}, // a literal percent: must match nothing, not everything
	}
	for _, f := range filters {
		txns, err := s.Transactions(f, 1000)
		if err != nil {
			t.Fatal(err)
		}
		tot, err := s.Totals(f)
		if err != nil {
			t.Fatal(err)
		}
		var in, out int64
		for _, tx := range txns {
			if tx.AmountCents > 0 {
				in += tx.AmountCents
			} else {
				out += tx.AmountCents
			}
		}
		if tot.Count != len(txns) || tot.InCents != in || tot.OutCents != out {
			t.Errorf("%+v: totals %+v, list has %d rows in %d out %d", f, tot, len(txns), in, out)
		}
	}

	if txns, _ := s.Transactions(ledger.Filter{Query: "monthly account"}, 100); len(txns) != 2 {
		t.Errorf("case-insensitive search found %d", len(txns))
	}
	if txns, _ := s.Transactions(ledger.Filter{Query: "50%"}, 100); len(txns) != 0 {
		t.Errorf("%% was treated as a wildcard: %d rows", len(txns))
	}
	months, _ := s.Months()
	if want := []string{"2026-10", "2026-09", "2026-08", "2026-07"}; !slices.Equal(months, want) {
		t.Errorf("months = %v, want %v", months, want)
	}
}

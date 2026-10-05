package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// The fixtures in testdata are synthetic, laid out exactly like real FNB exports
// (see docs/fnb-flow.md) with invented names, numbers and amounts.

func parseFile(t *testing.T, name string) Statement {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	sts, err := Parse(File{Name: name, Data: data})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(sts) != 1 {
		t.Fatalf("%s: %d statements, want 1", name, len(sts))
	}
	return sts[0]
}

func TestChequeCSV(t *testing.T) {
	st := parseFile(t, "cheque.csv")
	if st.AccountRef != "62000000001" || st.AccountLabel != "Cheque Acc" || st.Format != "csv" {
		t.Fatalf("header: %+v", st)
	}
	if len(st.Txns) != 6 {
		t.Fatalf("%d transactions, want 6", len(st.Txns))
	}
	first := st.Txns[0]
	if first.Date != "2026-10-02" || first.AmountCents != -25000 || first.Description != "SAVINGS TRANSFER" {
		t.Errorf("first row: %+v", first)
	}
	if first.BalanceCents == nil || *first.BalanceCents != 1000000 {
		t.Errorf("cheque account lost its running balance: %v", first.BalanceCents)
	}
	// FNB pads inside descriptions; collapsed, the formats agree.
	if got := st.Txns[1].Description; got != "CHECKERS SANDTON SANDTON ZA" {
		t.Errorf("description not collapsed: %q", got)
	}
}

// The card's balance column is all zeros. Recording R0.00 as the balance after
// every purchase would be a lie on every row.
func TestCardCSVHasNoBalance(t *testing.T) {
	st := parseFile(t, "card.csv")
	if st.AccountRef != "400000******1234" || st.AccountLabel != "Credit Acc" {
		t.Fatalf("header: %+v", st)
	}
	for _, tx := range st.Txns {
		if tx.BalanceCents != nil {
			t.Fatalf("card transaction has balance %d", *tx.BalanceCents)
		}
	}
}

func TestWindows1252(t *testing.T) {
	for _, name := range []string{"card.csv", "card.ofx"} {
		st := parseFile(t, name)
		if got := st.Txns[1].Description; got != "CAFÉ DELUXE CAPE TOWN ZA" {
			t.Errorf("%s: 0xC9 decoded as %q", name, got)
		}
		if got := st.Txns[5].Description; got != "SPOTIFY P’S BILL STOCKHOLM SE" {
			t.Errorf("%s: 0x92 decoded as %q", name, got)
		}
	}
}

func TestQuickOFX(t *testing.T) {
	st := parseFile(t, "cheque.ofx")
	if st.AccountRef != "62000000001" || st.Format != "ofx" || len(st.Txns) != 6 {
		t.Fatalf("got %+v", st)
	}
	if st.Txns[0].FITID == "" {
		t.Error("FITID not kept")
	}
}

// FNB's FITID is a content hash, so an identical same-day pair shares one. Both
// are real purchases and both must survive parsing.
func TestOFXKeepsCollidingFITIDs(t *testing.T) {
	st := parseFile(t, "card.ofx")
	if len(st.Txns) != 6 {
		t.Fatalf("%d transactions, want 6", len(st.Txns))
	}
	if st.Txns[1].FITID != st.Txns[2].FITID {
		t.Fatal("fixture no longer has a FITID collision")
	}
}

func TestAdvancedOFX(t *testing.T) {
	st := parseFile(t, "cheque-advanced.ofx")
	if st.AccountRef != "62000000001" || st.AccountLabel != "Cheque Acc" || st.Format != "ofx-advanced" {
		t.Fatalf("header: %+v", st)
	}
	if len(st.Txns) != 8 {
		t.Fatalf("%d transactions, want 8", len(st.Txns))
	}
	if st.Txns[7].Date != "2026-07-01" || st.Txns[7].AmountCents != -4500 {
		t.Errorf("last record: %+v", st.Txns[7])
	}
	if len(st.Txns[0].FITID) != 32 {
		t.Errorf("bare id line not captured: %q", st.Txns[0].FITID)
	}
}

type key struct {
	date, desc string
	cents      int64
}

func multiset(txns []Txn, from, to string) map[key]int {
	m := map[key]int{}
	for _, tx := range txns {
		if tx.Date >= from && tx.Date <= to {
			m[key{tx.Date, tx.Description, tx.AmountCents}]++
		}
	}
	return m
}

// The property the whole dedup design rests on, and which held for the real
// exports: over the window they share, every format describes the same
// transactions with the same (date, amount, description).
func TestFormatsAgree(t *testing.T) {
	csv := multiset(parseFile(t, "cheque.csv").Txns, "2026-08-14", "2026-10-02")
	ofx := multiset(parseFile(t, "cheque.ofx").Txns, "2026-08-14", "2026-10-02")
	adv := multiset(parseFile(t, "cheque-advanced.ofx").Txns, "2026-08-14", "2026-10-02")
	if len(csv) == 0 {
		t.Fatal("no transactions in the window")
	}
	for k, n := range csv {
		if ofx[k] != n || adv[k] != n {
			t.Errorf("%v: csv %d, ofx %d, advanced %d", k, n, ofx[k], adv[k])
		}
	}
	if len(ofx) != len(csv) || len(adv) != len(csv) {
		t.Errorf("distinct rows: csv %d, ofx %d, advanced %d", len(csv), len(ofx), len(adv))
	}
}

// The real card exports disagreed until OFX entities were decoded: OFX writes
// "&amp;" where the CSV has "&". The fixture carries one such merchant.
func TestCardFormatsAgree(t *testing.T) {
	csv := multiset(parseFile(t, "card.csv").Txns, "0", "9")
	ofx := multiset(parseFile(t, "card.ofx").Txns, "0", "9")
	if len(csv) == 0 || len(csv) != len(ofx) {
		t.Fatalf("distinct rows: csv %d, ofx %d", len(csv), len(ofx))
	}
	for k, n := range csv {
		if ofx[k] != n {
			t.Errorf("%v: csv %d, ofx %d", k, n, ofx[k])
		}
	}
	if csv[key{"2026-09-10", "PNP MELROSE ARCH & HYDE PARK ZA", -31000}] != 1 {
		t.Error("fixture no longer has the ampersand merchant")
	}
}

func TestEmptyStatement(t *testing.T) {
	st := parseFile(t, "empty.ofx")
	if st.AccountRef != "62000000001" || len(st.Txns) != 0 {
		t.Fatalf("got %+v", st)
	}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// FNB downloads arrive as a ZIP holding one file; uploading it as-is must work.
func TestZip(t *testing.T) {
	data, _ := os.ReadFile("testdata/cheque.ofx")
	sts, err := Parse(File{Name: "transaction_history_Cheque_Acc.zip", Data: zipOf(t, map[string][]byte{"62000000001.ofx": data})})
	if err != nil || len(sts) != 1 || len(sts[0].Txns) != 6 {
		t.Fatalf("zip: %d statements, err %v", len(sts), err)
	}
}

func TestZipRefusesOversizedEntry(t *testing.T) {
	big := bytes.Repeat([]byte("x"), maxEntryBytes+10) // compresses to almost nothing
	_, err := Parse(File{Name: "bomb.zip", Data: zipOf(t, map[string][]byte{"a.csv": big})})
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized entry: %v", err)
	}
}

func TestNotRecognised(t *testing.T) {
	_, err := Parse(File{Name: "notes.txt", Data: []byte("hello")})
	if !errors.Is(err, ErrNotRecognised) {
		t.Fatalf("got %v", err)
	}
}

func TestCSVWithoutAccountLineIsRefused(t *testing.T) {
	_, err := Parse(File{Data: []byte("Date, Amount, Balance, Description\n2026/10/01, -1.00, 0, X\n")})
	if err == nil || !strings.Contains(err.Error(), "Account:") {
		t.Fatalf("got %v", err)
	}
}

func TestCSVBadRowNamesTheLine(t *testing.T) {
	data, _ := os.ReadFile("testdata/cheque.csv")
	bad := strings.Replace(string(data), "-250.00", "-25O.00", 1)
	_, err := Parse(File{Data: []byte(bad)})
	if err == nil || !strings.Contains(err.Error(), "line 8") {
		t.Fatalf("got %v", err)
	}
}

func TestParseCents(t *testing.T) {
	good := map[string]int64{
		"0": 0, "12": 1200, "12.5": 1250, "12.05": 1205, "-1234.56": -123456, "+3.10": 310, " 7.00 ": 700,
		// The classic float trap: 0.29 * 100 is 28.999999999999996.
		"0.29": 29,
	}
	for in, want := range good {
		if got, err := parseCents(in); err != nil || got != want {
			t.Errorf("parseCents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-", "1.234", "1.", ".5", "1,000.00", "abc", "1e3", "99999999999999999999"} {
		if _, err := parseCents(in); err == nil {
			t.Errorf("parseCents(%q) accepted", in)
		}
	}
}

func TestIsoDate(t *testing.T) {
	good := map[string]string{
		"2026/10/05": "2026-10-05", "20261005": "2026-10-05", "2026-10-05": "2026-10-05",
		"20261005120000[+2:SAST]": "2026-10-05",
	}
	for in, want := range good {
		if got, err := isoDate(in); err != nil || got != want {
			t.Errorf("isoDate(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "2026/13/01", "2026/00/10", "2026/10/32", "05/10/2026", "2026x1005"} {
		if _, err := isoDate(in); err == nil {
			t.Errorf("isoDate(%q) accepted", in)
		}
	}
}

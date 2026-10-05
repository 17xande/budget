package handler_test

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
)

// A merchant names itself; a description beginning "=" must reach a spreadsheet
// as text, never as a live formula.
const formulaCSV = `ACCOUNT TRANSACTION HISTORY

Name:, Test, Person
Account:, 62000000009, [Cheque Acc]
Balance:, 100.00, 100.00

Date, Amount, Balance, Description
2026/10/03, -10.00, 100.00, =HYPERLINK("http://evil.example","click")
2026/10/02, -20.00, 110.00, @SUM(A1:A9)
2026/10/01, -30.00, 130.00, PLAIN SHOP
`

func TestTransactionsCSV(t *testing.T) {
	_, owner := seeded(t)
	owner.upload(map[string][]byte{"formula.csv": []byte(formulaCSV)})

	r := owner.get("/transactions.csv?month=all")
	if r.code != http.StatusOK || !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/csv") ||
		!strings.Contains(r.hdr.Get("Content-Disposition"), `filename="transactions-all.csv"`) {
		t.Fatalf("export: %d %v", r.code, r.hdr)
	}
	rows, err := csv.NewReader(strings.NewReader(r.body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 || rows[0][0] != "Date" || rows[0][3] != "Amount" {
		t.Fatalf("%d rows, header %v", len(rows), rows[0])
	}
	// Oldest first, as a ledger reads.
	if rows[1][0] > rows[len(rows)-1][0] {
		t.Errorf("not oldest first: %s .. %s", rows[1][0], rows[len(rows)-1][0])
	}
	// Within a day the bank's order survives, so the running balance reads
	// correctly: the cheque fixture's two same-day Checkers rows, 10339.90 then 10250.00.
	var checkers []string
	for _, row := range rows[1:] {
		if strings.HasPrefix(row[2], "CHECKERS") {
			checkers = append(checkers, row[4])
		}
	}
	if len(checkers) != 2 || checkers[0] != "10339.90" || checkers[1] != "10250.00" {
		t.Errorf("same-day rows out of order: balances %v", checkers)
	}
	descs := map[string]bool{}
	for _, row := range rows[1:] {
		descs[row[2]] = true
		// Amounts are ours: numbers, unescaped, so a spreadsheet can sum them.
		if strings.HasPrefix(row[3], "'") {
			t.Errorf("amount escaped: %v", row)
		}
		for _, cell := range []string{row[1], row[2], row[5]} {
			if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
				t.Errorf("live formula cell %q", cell)
			}
		}
	}
	if !descs[`'=HYPERLINK("http://evil.example","click")`] || !descs["'@SUM(A1:A9)"] || !descs["PLAIN SHOP"] {
		t.Errorf("descriptions not escaped as expected: %v", descs)
	}

	// The page's link exports what the page shows: here the latest month only.
	page := owner.get("/transactions").body
	if !strings.Contains(page, `href="/transactions.csv?month=2026-10"`) {
		t.Error("the page's export link is not its resolved filter")
	}
	month := owner.get("/transactions.csv?month=2026-10&q=plain")
	if rows, _ := csv.NewReader(strings.NewReader(month.body)).ReadAll(); len(rows) != 2 || rows[1][2] != "PLAIN SHOP" {
		t.Errorf("filtered export: %v", rows)
	}
}

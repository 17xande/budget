package importer

import (
	"errors"
	"fmt"
	"strings"
)

// parseCSV reads FNB's transaction-history CSV:
//
//	ACCOUNT TRANSACTION HISTORY
//
//	Name:, <first>, <last>
//	Account:, 62000000001, [Cheque Acc]
//	Balance:, 1234.56, 1234.56
//
//	Date, Amount, Balance, Description
//	2026/10/05, -123.45, 1111.11, SOME MERCHANT     CITY   ZA
//
// Fields are separated by ", " and nothing is quoted. Description is the last
// field and is taken whole, so a comma inside one cannot shift the columns.
func parseCSV(text string) (Statement, error) {
	st := Statement{Format: "csv"}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	header := -1
	for i, l := range lines {
		key, rest, _ := strings.Cut(l, ",")
		switch strings.TrimSpace(key) {
		case "Account:":
			// "62000000001, [Cheque Acc]"
			ref, label, _ := strings.Cut(rest, ",")
			st.AccountRef = strings.TrimSpace(ref)
			st.AccountLabel = strings.Trim(strings.TrimSpace(label), "[]")
		case "Date":
			if strings.ReplaceAll(l, " ", "") == "Date,Amount,Balance,Description" {
				header = i
			}
		}
		if header >= 0 {
			break
		}
	}
	if header < 0 {
		return st, errors.New("no 'Date, Amount, Balance, Description' header row")
	}
	if st.AccountRef == "" {
		// Phase 2 cannot ask which account a file belongs to, so a CSV without
		// its account line is refused rather than guessed.
		return st, errors.New("no 'Account:' line, so the file does not say which account it is")
	}

	var balances []int64
	for i, l := range lines[header+1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		f := strings.SplitN(l, ",", 4)
		if len(f) != 4 {
			return st, fmt.Errorf("line %d: want 4 fields, got %d", header+2+i, len(f))
		}
		date, err := isoDate(f[0])
		if err != nil {
			return st, fmt.Errorf("line %d: %w", header+2+i, err)
		}
		amount, err := parseCents(f[1])
		if err != nil {
			return st, fmt.Errorf("line %d: %w", header+2+i, err)
		}
		bal, err := parseCents(f[2])
		if err != nil {
			return st, fmt.Errorf("line %d: balance: %w", header+2+i, err)
		}
		balances = append(balances, bal)
		st.Txns = append(st.Txns, Txn{Date: date, AmountCents: amount, Description: collapse(f[3])})
	}

	// The credit card's balance column is all zeros: that is "no running
	// balance", not a card permanently at R0.00. A real account at exactly zero
	// on every row is indistinguishable and loses nothing by being dropped.
	allZero := true
	for _, b := range balances {
		if b != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		for i := range st.Txns {
			st.Txns[i].BalanceCents = &balances[i]
		}
	}
	return st, nil
}

// Package ledger holds the bank accounts, the transactions imported into them and
// the history of imports.
package ledger

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/importer"
)

// Kinds of account. Spending reports will count cheque and credit card accounts;
// moving money into savings or paying a loan is not spending.
var Kinds = []string{"cheque", "credit", "savings", "loan", "other"}

func ValidKind(k string) bool {
	for _, v := range Kinds {
		if k == v {
			return true
		}
	}
	return false
}

type Account struct {
	ID        int64
	Ref       string
	Name      string
	Kind      string
	Fetch     bool // imported by the daily FNB fetch
	CreatedAt time.Time
	// Summary, filled by Accounts.
	TxnCount  int
	FirstDate string
	LastDate  string
}

type Txn struct {
	ID           int64
	AccountID    int64
	AccountName  string
	PostedOn     string
	AmountCents  int64
	Description  string
	BalanceCents sql.NullInt64
	CategoryID   sql.NullInt64
	CategoryName string
	// CategorySource is who chose the category: "rule", "manual", "ai", or ""
	// when there is none.
	CategorySource string
}

// Source is where an import came from.
type Source string

const (
	SourceUpload Source = "upload"
	SourceFNB    Source = "fnb"
)

type Import struct {
	ID          int64
	Source      Source
	Filename    string
	Format      string
	AccountName string // "" when the file failed before naming an account
	UserName    string // "" for the automatic fetch
	Inserted    int
	Skipped     int
	Error       string
	CreatedAt   time.Time
}

// DedupKeys returns each transaction's identity within the ledger:
// the account, the date, the amount, the description, and how many identical
// transactions came before it in the same statement.
//
// The occurrence index is what keeps two identical purchases on one day -- two
// coffees -- as two rows while re-importing the same statement adds nothing: a
// later export of that day lists the same pair, so its keys are the same pair.
// It is content-based because content is what FNB's formats agree on (see
// docs/fnb-flow.md); FITIDs are not, and do not distinguish the pair anyway.
func DedupKeys(accountID int64, txns []importer.Txn) []string {
	seen := map[string]int{}
	keys := make([]string, len(txns))
	for i, tx := range txns {
		base := fmt.Sprintf("%d\x00%s\x00%d\x00%s", accountID, tx.Date, tx.AmountCents, tx.Description)
		n := seen[base]
		seen[base] = n + 1
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", base, n)))
		keys[i] = hex.EncodeToString(sum[:])
	}
	return keys
}

// defaultAccount names and classifies an account from what the file says about
// it. Only used when the account is first seen; renaming it later sticks.
func defaultAccount(ref, label string) (name, kind string) {
	tail := ref
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	l := strings.ToLower(label)
	switch {
	case strings.HasPrefix(l, "cheque"):
		return "Cheque account ···" + tail, "cheque"
	case strings.HasPrefix(l, "credit"), strings.Contains(ref, "*"):
		// OFX carries no label, but only a card number is masked.
		return "Credit card ···" + tail, "credit"
	case strings.HasPrefix(l, "savings"):
		return "Savings ···" + tail, "savings"
	default:
		return "Account ···" + tail, "other"
	}
}

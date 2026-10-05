// Package budget is what turns a ledger into a budget: categories, the rules that
// fill them in, monthly targets, and the summary of a month against those
// targets. Every change it makes is written to the audit log with the value it
// replaced.
package budget

import (
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Kinds of category, and how reports treat each.
const (
	KindExpense  = "expense"  // counts as spending; a refund here reduces it
	KindIncome   = "income"   // counts as income
	KindTransfer = "transfer" // money between your own accounts: neither
)

var Kinds = []string{KindExpense, KindIncome, KindTransfer}

func ValidKind(k string) bool {
	return k == KindExpense || k == KindIncome || k == KindTransfer
}

type Category struct {
	ID       int64
	Name     string
	Kind     string
	Archived bool
}

type Rule struct {
	ID           int64
	Pattern      string
	CategoryID   int64
	CategoryName string
	AccountID    sql.NullInt64 // NULL: every account
	AccountName  string
	// Matches is how many transactions currently carry this rule's category
	// and match its pattern -- a rough "is this rule doing anything".
	Matches int
}

// Via is the door a change came through.
type Via string

const (
	ViaWeb Via = "web"
	ViaMCP Via = "mcp"
)

// Actor is who made a change, for the audit log.
type Actor struct {
	UserID    int64
	Via       Via
	TokenName string // the API token's name, for changes through MCP
}

// source is how a transaction's category is recorded as chosen.
func (a Actor) source() string {
	if a.Via == ViaMCP {
		return "ai"
	}
	return "manual"
}

var (
	ErrNotFound  = errors.New("not found")
	ErrNameTaken = errors.New("a category with that name already exists")
	ErrArchived  = errors.New("that category is archived")
	ErrInvalid   = errors.New("invalid")
)

// MaxNameLength bounds category names and rule patterns.
const MaxNameLength = 60

// CleanName trims and collapses a name or pattern the way descriptions are
// collapsed on import, so "WOOLWORTHS  FOOD" matches what is stored.
func CleanName(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func validName(s string) bool {
	return s != "" && utf8.RuneCountInString(s) <= MaxNameLength
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// likePattern turns a user's text into a LIKE operand matching it anywhere, with
// LIKE's own wildcards escaped: a rule for "50%" means the text.
func likePattern(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
}

// StarterCategories is offered once, on an empty budget, so there is something
// to categorise into on day one. Every one can be renamed or archived.
var StarterCategories = []Category{
	{Name: "Groceries", Kind: KindExpense},
	{Name: "Eating out", Kind: KindExpense},
	{Name: "Fuel & transport", Kind: KindExpense},
	{Name: "Home loan / rent", Kind: KindExpense},
	{Name: "Utilities", Kind: KindExpense},
	{Name: "Phone & internet", Kind: KindExpense},
	{Name: "Insurance", Kind: KindExpense},
	{Name: "Medical", Kind: KindExpense},
	{Name: "Subscriptions", Kind: KindExpense},
	{Name: "Shopping", Kind: KindExpense},
	{Name: "Entertainment", Kind: KindExpense},
	{Name: "Giving", Kind: KindExpense},
	{Name: "Bank fees", Kind: KindExpense},
	{Name: "Other spending", Kind: KindExpense},
	{Name: "Salary", Kind: KindIncome},
	{Name: "Other income", Kind: KindIncome},
	{Name: "Transfers", Kind: KindTransfer},
}

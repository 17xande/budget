package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/importer"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// nullID is a user id for an import: zero (the automatic fetch) is NULL.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Import records one statement: the account (created on first sight), an import
// row, and every transaction not already in the ledger. One transaction, so a
// failure part-way leaves nothing behind and the file can simply be uploaded
// again.
//
// Duplicates are refused by the insert itself (ON CONFLICT DO NOTHING on
// dedup_key) rather than by a lookup first, which would race the daily fetch.
func (s *Store) Import(st importer.Statement, src Source, filename string, userID int64, now time.Time) (Import, error) {
	if st.AccountRef == "" {
		return Import{}, errors.New("statement names no account")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Import{}, err
	}
	defer tx.Rollback()

	name, kind := defaultAccount(st.AccountRef, st.AccountLabel)
	if _, err := tx.Exec(`INSERT INTO accounts (ref, name, kind, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(ref) DO NOTHING`, st.AccountRef, name, kind, formatTime(now)); err != nil {
		return Import{}, fmt.Errorf("upsert account: %w", err)
	}
	var accountID int64
	if err := tx.QueryRow(`SELECT id, name FROM accounts WHERE ref = ?`, st.AccountRef).Scan(&accountID, &name); err != nil {
		return Import{}, fmt.Errorf("find account: %w", err)
	}

	res, err := tx.Exec(`INSERT INTO imports (source, filename, format, account_id, user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, string(src), filename, st.Format, accountID, nullID(userID), formatTime(now))
	if err != nil {
		return Import{}, fmt.Errorf("insert import: %w", err)
	}
	importID, _ := res.LastInsertId()

	stmt, err := tx.Prepare(`INSERT INTO transactions
		(account_id, posted_on, amount_cents, description, balance_cents, fitid, import_id, dedup_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedup_key) DO NOTHING`)
	if err != nil {
		return Import{}, err
	}
	defer stmt.Close()

	imp := Import{ID: importID, Source: src, Filename: filename, Format: st.Format, AccountName: name, CreatedAt: now.UTC()}
	keys := DedupKeys(accountID, st.Txns)
	for i, t := range st.Txns {
		var bal any
		if t.BalanceCents != nil {
			bal = *t.BalanceCents
		}
		r, err := stmt.Exec(accountID, t.Date, t.AmountCents, t.Description, bal, t.FITID, importID, keys[i], formatTime(now))
		if err != nil {
			return Import{}, fmt.Errorf("insert transaction %s %d: %w", t.Date, t.AmountCents, err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			imp.Inserted++
		} else {
			imp.Skipped++
		}
	}
	if _, err := tx.Exec(`UPDATE imports SET inserted = ?, skipped = ? WHERE id = ?`, imp.Inserted, imp.Skipped, importID); err != nil {
		return Import{}, err
	}
	return imp, tx.Commit()
}

// RecordFailure keeps a file that could not be imported in the history, with why,
// so a failed upload or fetch is visible rather than silently missing.
func (s *Store) RecordFailure(src Source, filename string, userID int64, cause error, now time.Time) error {
	_, err := s.db.Exec(`INSERT INTO imports (source, filename, user_id, error, created_at) VALUES (?, ?, ?, ?, ?)`,
		string(src), filename, nullID(userID), cause.Error(), formatTime(now))
	return err
}

// Imports returns the most recent imports, newest first.
func (s *Store) Imports(limit int) ([]Import, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.source, i.filename, i.format, COALESCE(a.name, ''), COALESCE(u.name, ''),
		       i.inserted, i.skipped, i.error, i.created_at
		FROM imports i
		LEFT JOIN accounts a ON a.id = i.account_id
		LEFT JOIN users u ON u.id = i.user_id
		ORDER BY i.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Import
	for rows.Next() {
		var im Import
		var created string
		if err := rows.Scan(&im.ID, &im.Source, &im.Filename, &im.Format, &im.AccountName, &im.UserName,
			&im.Inserted, &im.Skipped, &im.Error, &created); err != nil {
			return nil, err
		}
		im.CreatedAt = parseTime(created)
		out = append(out, im)
	}
	return out, rows.Err()
}

// Accounts returns every account with its transaction count and date range.
func (s *Store) Accounts() ([]Account, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.ref, a.name, a.kind, a.created_at,
		       count(t.id), COALESCE(min(t.posted_on), ''), COALESCE(max(t.posted_on), '')
		FROM accounts a LEFT JOIN transactions t ON t.account_id = a.id
		GROUP BY a.id ORDER BY a.kind, a.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		var created string
		if err := rows.Scan(&a.ID, &a.Ref, &a.Name, &a.Kind, &created, &a.TxnCount, &a.FirstDate, &a.LastDate); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAccount renames and reclassifies an account, reporting whether it exists.
func (s *Store) UpdateAccount(id int64, name, kind string) (bool, error) {
	if !ValidKind(kind) {
		return false, fmt.Errorf("not an account kind: %q", kind)
	}
	res, err := s.db.Exec(`UPDATE accounts SET name = ?, kind = ? WHERE id = ?`, name, kind, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Months returns every month with transactions, newest first, as YYYY-MM.
func (s *Store) Months() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT substr(posted_on, 1, 7) FROM transactions ORDER BY 1 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Filter selects transactions. Zero values mean "any".
type Filter struct {
	Month      string // YYYY-MM
	From, To   string // YYYY-MM-DD, inclusive; for date ranges other than a month
	AccountID  int64
	CategoryID int64
	// Uncategorised selects only transactions without a category. It wins
	// over CategoryID.
	Uncategorised bool
	Query         string // substring of the description, case-insensitive
}

// where builds the one WHERE clause both Transactions and Totals use. A list and
// its totals must read exactly the same rows, or the page claims numbers it does
// not show; sharing the builder is what keeps them from drifting apart.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Month != "" {
		conds = append(conds, "t.posted_on LIKE ?")
		args = append(args, f.Month+"-%")
	}
	if f.From != "" {
		conds = append(conds, "t.posted_on >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		conds = append(conds, "t.posted_on <= ?")
		args = append(args, f.To)
	}
	if f.AccountID != 0 {
		conds = append(conds, "t.account_id = ?")
		args = append(args, f.AccountID)
	}
	switch {
	case f.Uncategorised:
		conds = append(conds, "t.category_id IS NULL")
	case f.CategoryID != 0:
		conds = append(conds, "t.category_id = ?")
		args = append(args, f.CategoryID)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		// LIKE treats % and _ as wildcards; a search for "50%" means the text.
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
		conds = append(conds, `t.description LIKE ? ESCAPE '\'`)
		args = append(args, "%"+esc+"%")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// Transactions returns matching transactions, newest first, at most limit of them.
func (s *Store) Transactions(f Filter, limit int) ([]Txn, error) {
	return s.TransactionsPage(f, limit, 0)
}

// TransactionsPage is Transactions starting after offset rows, for paging.
func (s *Store) TransactionsPage(f Filter, limit, offset int) ([]Txn, error) {
	where, args := f.where()
	rows, err := s.db.Query(`
		SELECT t.id, t.account_id, a.name, t.posted_on, t.amount_cents, t.description, t.balance_cents,
		       t.category_id, COALESCE(c.name, ''), t.category_source
		FROM transactions t JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		`+where+`
		ORDER BY t.posted_on DESC, t.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Txn
	for rows.Next() {
		var t Txn
		if err := rows.Scan(&t.ID, &t.AccountID, &t.AccountName, &t.PostedOn, &t.AmountCents, &t.Description, &t.BalanceCents,
			&t.CategoryID, &t.CategoryName, &t.CategorySource); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Totals summarises the transactions a filter matches.
type Totals struct {
	Count    int
	InCents  int64 // money in, positive
	OutCents int64 // money out, negative
}

func (t Totals) NetCents() int64 { return t.InCents + t.OutCents }

func (s *Store) Totals(f Filter) (Totals, error) {
	where, args := f.where()
	var tot Totals
	err := s.db.QueryRow(`
		SELECT count(*),
		       COALESCE(sum(CASE WHEN t.amount_cents > 0 THEN t.amount_cents END), 0),
		       COALESCE(sum(CASE WHEN t.amount_cents < 0 THEN t.amount_cents END), 0)
		FROM transactions t JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		`+where, args...).Scan(&tot.Count, &tot.InCents, &tot.OutCents)
	return tot, err
}

// Txn returns one transaction.
func (s *Store) Txn(id int64) (Txn, error) {
	var t Txn
	err := s.db.QueryRow(`
		SELECT t.id, t.account_id, a.name, t.posted_on, t.amount_cents, t.description, t.balance_cents,
		       t.category_id, COALESCE(c.name, ''), t.category_source
		FROM transactions t JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.id = ?`, id).Scan(&t.ID, &t.AccountID, &t.AccountName, &t.PostedOn, &t.AmountCents, &t.Description,
		&t.BalanceCents, &t.CategoryID, &t.CategoryName, &t.CategorySource)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// ErrNotFound means no such row.
var ErrNotFound = errors.New("not found")

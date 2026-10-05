package budget

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// execer is what audit needs from a *sql.Tx or *sql.DB.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// audit records one change. It runs inside the change's own transaction, so a
// change and its record are never separated.
func (s *Store) audit(q execer, a Actor, action string, subject int64, before, after string) error {
	_, err := q.Exec(`INSERT INTO audit_log (user_id, via, token_name, action, subject_id, before, after, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nullID(a.UserID), string(a.Via), a.TokenName, action, subject, before, after, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

// --- Categories ----------------------------------------------------------------

// Categories returns categories ordered by kind then name; archived ones only
// when asked.
func (s *Store) Categories(includeArchived bool) ([]Category, error) {
	rows, err := s.db.Query(`SELECT id, name, kind, archived FROM categories
		WHERE archived = 0 OR ? ORDER BY archived, CASE kind WHEN 'expense' THEN 0 WHEN 'income' THEN 1 ELSE 2 END, name COLLATE NOCASE`,
		includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Archived); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Category returns one category.
func (s *Store) Category(id int64) (Category, error) {
	var c Category
	err := s.db.QueryRow(`SELECT id, name, kind, archived FROM categories WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Kind, &c.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CreateCategory adds a category. A duplicate name (case-insensitively) is
// ErrNameTaken, refused by the insert itself rather than a lookup first.
func (s *Store) CreateCategory(name, kind string, a Actor) (Category, error) {
	name = CleanName(name)
	if !validName(name) || !ValidKind(kind) {
		return Category{}, ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Category{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO categories (name, kind, created_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO NOTHING`, name, kind, formatTime(s.now()))
	if err != nil {
		return Category{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Category{}, ErrNameTaken
	}
	id, _ := res.LastInsertId()
	if err := s.audit(tx, a, "category.create", id, "", name+" ("+kind+")"); err != nil {
		return Category{}, err
	}
	return Category{ID: id, Name: name, Kind: kind}, tx.Commit()
}

// UpdateCategory renames a category and changes its kind.
func (s *Store) UpdateCategory(id int64, name, kind string, a Actor) error {
	name = CleanName(name)
	if !validName(name) || !ValidKind(kind) {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldName, oldKind string
	err = tx.QueryRow(`SELECT name, kind FROM categories WHERE id = ?`, id).Scan(&oldName, &oldKind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	// The uniqueness guard is in the statement: renaming onto another
	// category's name fails here rather than after a racy lookup.
	if _, err := tx.Exec(`UPDATE categories SET name = ?, kind = ? WHERE id = ?
		AND NOT EXISTS (SELECT 1 FROM categories WHERE name = ? AND id <> ?)`, name, kind, id, name, id); err != nil {
		return err
	}
	var gotName string
	if err := tx.QueryRow(`SELECT name FROM categories WHERE id = ?`, id).Scan(&gotName); err != nil {
		return err
	}
	if gotName != name {
		return ErrNameTaken
	}
	if oldName != name || oldKind != kind {
		if err := s.audit(tx, a, "category.update", id, oldName+" ("+oldKind+")", name+" ("+kind+")"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetArchived archives or restores a category. Archiving keeps its history:
// transactions keep the category, it just stops being offered.
func (s *Store) SetArchived(id int64, archived bool, a Actor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE categories SET archived = ? WHERE id = ?`, archived, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	action := "category.restore"
	if archived {
		action = "category.archive"
	}
	if err := s.audit(tx, a, action, id, "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

// SeedStarter adds the starter categories, skipping any name already present,
// and reports how many it added.
func (s *Store) SeedStarter(a Actor) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	added := 0
	for _, c := range StarterCategories {
		res, err := tx.Exec(`INSERT INTO categories (name, kind, created_at) VALUES (?, ?, ?)
			ON CONFLICT(name) DO NOTHING`, c.Name, c.Kind, formatTime(s.now()))
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	if added > 0 {
		if err := s.audit(tx, a, "category.starter", 0, "", strconv.Itoa(added)+" categories"); err != nil {
			return 0, err
		}
	}
	return added, tx.Commit()
}

// --- Categorising ----------------------------------------------------------------

// Categorise sets one transaction's category (0 clears it), reporting whether
// anything changed. The previous value goes to the audit log, which is what
// makes the change undoable.
func (s *Store) Categorise(txnID, categoryID int64, a Actor) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed, err := s.categorise(tx, txnID, categoryID, a)
	if err != nil {
		return false, err
	}
	return changed, tx.Commit()
}

// CategoriseMany applies a batch atomically: every change, or none.
func (s *Store) CategoriseMany(changes map[int64]int64, a Actor) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for txnID, catID := range changes {
		changed, err := s.categorise(tx, txnID, catID, a)
		if err != nil {
			return 0, fmt.Errorf("transaction %d: %w", txnID, err)
		}
		if changed {
			n++
		}
	}
	return n, tx.Commit()
}

func (s *Store) categorise(tx *sql.Tx, txnID, categoryID int64, a Actor) (bool, error) {
	var before sql.NullInt64
	err := tx.QueryRow(`SELECT category_id FROM transactions WHERE id = ?`, txnID).Scan(&before)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, err
	}
	if categoryID != 0 {
		var archived bool
		err := tx.QueryRow(`SELECT archived FROM categories WHERE id = ?`, categoryID).Scan(&archived)
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("category %d: %w", categoryID, ErrNotFound)
		} else if err != nil {
			return false, err
		}
		if archived && before.Int64 != categoryID {
			return false, ErrArchived
		}
	}
	if before.Int64 == categoryID {
		return false, nil
	}
	source := a.source()
	if categoryID == 0 {
		source = ""
	}
	if _, err := tx.Exec(`UPDATE transactions SET category_id = ?, category_source = ? WHERE id = ?`,
		nullID(categoryID), source, txnID); err != nil {
		return false, err
	}
	if err := s.audit(tx, a, "categorise", txnID, idText(before.Int64), idText(categoryID)); err != nil {
		return false, err
	}
	return true, nil
}

func idText(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// --- Rules -----------------------------------------------------------------------

// Rules returns every rule, longest pattern first -- the order they apply in.
func (s *Store) Rules() ([]Rule, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.pattern, r.category_id, c.name, r.account_id, COALESCE(a.name, ''),
		       (SELECT count(*) FROM transactions t
		        WHERE t.category_id = r.category_id
		          AND t.description LIKE '%' || replace(replace(replace(r.pattern, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
		          AND (r.account_id IS NULL OR t.account_id = r.account_id))
		FROM rules r JOIN categories c ON c.id = r.category_id
		LEFT JOIN accounts a ON a.id = r.account_id
		ORDER BY length(r.pattern) DESC, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Pattern, &r.CategoryID, &r.CategoryName, &r.AccountID, &r.AccountName, &r.Matches); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Rule(id int64) (Rule, error) {
	var r Rule
	err := s.db.QueryRow(`SELECT r.id, r.pattern, r.category_id, c.name, r.account_id
		FROM rules r JOIN categories c ON c.id = r.category_id WHERE r.id = ?`, id).
		Scan(&r.ID, &r.Pattern, &r.CategoryID, &r.CategoryName, &r.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// SaveRule creates (id 0) or updates a rule.
func (s *Store) SaveRule(id int64, pattern string, categoryID, accountID int64, a Actor) (int64, error) {
	pattern = CleanName(pattern)
	if !validName(pattern) {
		return 0, ErrInvalid
	}
	cat, err := s.Category(categoryID)
	if err != nil {
		return 0, err
	}
	if cat.Archived {
		return 0, ErrArchived
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	after := pattern + " → " + cat.Name
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO rules (pattern, category_id, account_id, created_at) VALUES (?, ?, ?, ?)`,
			pattern, categoryID, nullID(accountID), formatTime(s.now()))
		if err != nil {
			return 0, err
		}
		id, _ = res.LastInsertId()
		if err := s.audit(tx, a, "rule.create", id, "", after); err != nil {
			return 0, err
		}
	} else {
		old, err := s.Rule(id)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`UPDATE rules SET pattern = ?, category_id = ?, account_id = ? WHERE id = ?`,
			pattern, categoryID, nullID(accountID), id); err != nil {
			return 0, err
		}
		if err := s.audit(tx, a, "rule.update", id, old.Pattern+" → "+old.CategoryName, after); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (s *Store) DeleteRule(id int64, a Actor) error {
	old, err := s.Rule(id)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rules WHERE id = ?`, id); err != nil {
		return err
	}
	if err := s.audit(tx, a, "rule.delete", id, old.Pattern+" → "+old.CategoryName, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// Scope limits which transactions ApplyRules may touch. Zero values mean "all".
type Scope struct {
	ImportID int64 // only transactions from this import
	RuleID   int64 // only this rule
}

// ApplyRules fills in uncategorised transactions from the rules and reports how
// many it categorised. It never changes a transaction that already has a
// category -- a person's choice, or an earlier rule's, stands.
//
// Longest pattern first, each statement touching only rows still uncategorised,
// so the most specific matching rule is the one that sticks.
func (s *Store) ApplyRules(sc Scope) (int, error) {
	q := `SELECT id, pattern, category_id, account_id FROM rules`
	var args []any
	if sc.RuleID != 0 {
		q += ` WHERE id = ?`
		args = append(args, sc.RuleID)
	}
	q += ` ORDER BY length(pattern) DESC, id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return 0, err
	}
	type rule struct {
		pattern string
		cat     int64
		account sql.NullInt64
	}
	var rules []rule
	for rows.Next() {
		var r rule
		var id int64
		if err := rows.Scan(&id, &r.pattern, &r.cat, &r.account); err != nil {
			rows.Close()
			return 0, err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	total := 0
	for _, r := range rules {
		res, err := tx.Exec(`UPDATE transactions SET category_id = ?, category_source = 'rule'
			WHERE category_id IS NULL
			  AND description LIKE ? ESCAPE '\'
			  AND (? IS NULL OR account_id = ?)
			  AND (? = 0 OR import_id = ?)
			  AND EXISTS (SELECT 1 FROM categories WHERE id = ? AND archived = 0)`,
			r.cat, likePattern(r.pattern), r.account, r.account, sc.ImportID, sc.ImportID, r.cat)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, tx.Commit()
}

// --- Targets ---------------------------------------------------------------------

// Targets returns a month's targets by category.
func (s *Store) Targets(month string) (map[int64]int64, error) {
	rows, err := s.db.Query(`SELECT category_id, target_cents FROM budget_targets WHERE month = ?`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, c int64
		if err := rows.Scan(&id, &c); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// SetTarget sets (cents >= 0) or clears (cents < 0) one category's target for a
// month, auditing a real change.
func (s *Store) SetTarget(month string, categoryID, cents int64, a Actor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.setTarget(tx, month, categoryID, cents, a); err != nil {
		return err
	}
	return tx.Commit()
}

// SetTargets applies a whole month's form at once.
func (s *Store) SetTargets(month string, targets map[int64]int64, a Actor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for cat, cents := range targets {
		if err := s.setTarget(tx, month, cat, cents, a); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) setTarget(tx *sql.Tx, month string, categoryID, cents int64, a Actor) error {
	var kind string
	err := tx.QueryRow(`SELECT kind FROM categories WHERE id = ?`, categoryID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if kind == KindTransfer {
		return fmt.Errorf("%w: transfers have no target", ErrInvalid)
	}
	var old sql.NullInt64
	err = tx.QueryRow(`SELECT target_cents FROM budget_targets WHERE category_id = ? AND month = ?`, categoryID, month).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	before := ""
	if old.Valid {
		before = strconv.FormatInt(old.Int64, 10)
	}
	after := ""
	if cents < 0 {
		if !old.Valid {
			return nil
		}
		if _, err := tx.Exec(`DELETE FROM budget_targets WHERE category_id = ? AND month = ?`, categoryID, month); err != nil {
			return err
		}
	} else {
		if old.Valid && old.Int64 == cents {
			return nil
		}
		after = strconv.FormatInt(cents, 10)
		if _, err := tx.Exec(`INSERT INTO budget_targets (category_id, month, target_cents) VALUES (?, ?, ?)
			ON CONFLICT(category_id, month) DO UPDATE SET target_cents = excluded.target_cents`,
			categoryID, month, cents); err != nil {
			return err
		}
	}
	return s.audit(tx, a, "target.set", categoryID, month+" "+before, month+" "+after)
}

// CopyTargets copies one month's targets into another, never overwriting a
// target the destination already has. One statement, so it is safe to repeat.
func (s *Store) CopyTargets(from, to string, a Actor) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO budget_targets (category_id, month, target_cents)
		SELECT t.category_id, ?, t.target_cents FROM budget_targets t
		JOIN categories c ON c.id = t.category_id AND c.archived = 0
		WHERE t.month = ?
		ON CONFLICT(category_id, month) DO NOTHING`, to, from)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := s.audit(tx, a, "target.copy", 0, from, to+" ("+strconv.FormatInt(n, 10)+")"); err != nil {
			return 0, err
		}
	}
	return int(n), tx.Commit()
}

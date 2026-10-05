package budget

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Record writes an audit entry for a budgeting change made outside this package
// (renaming an account lives in the ledger), so the activity log is complete
// whichever store made the change.
func (s *Store) Record(a Actor, action string, subject int64, before, after string) error {
	return s.audit(s.db, a, action, subject, before, after)
}

// Entry is one audit log row, with names resolved for showing to a person.
type Entry struct {
	ID        int64
	When      time.Time
	UserName  string
	Via       Via
	TokenName string
	Action    string
	SubjectID int64
	Before    string
	After     string
	UndoneAt  time.Time // zero: not undone

	// For "categorise": the transaction, and the categories by name.
	Description string
	BeforeName  string
	AfterName   string
}

// CanUndo reports whether Undo can be offered. Only categorisations are
// undoable: they are the bulk of what an AI does, and the one change whose
// previous value fully describes how to reverse it.
func (e Entry) CanUndo() bool { return e.Action == "categorise" && e.UndoneAt.IsZero() }

// Activity returns recent changes, newest first; via "" for all of them.
func (s *Store) Activity(via Via, limit int) ([]Entry, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.created_at, COALESCE(u.name, ''), a.via, a.token_name, a.action,
		       COALESCE(a.subject_id, 0), a.before, a.after, a.undone_at,
		       COALESCE(t.description, ''), COALESCE(cb.name, ''), COALESCE(ca.name, '')
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.user_id
		LEFT JOIN transactions t ON a.action IN ('categorise', 'undo') AND t.id = a.subject_id
		LEFT JOIN categories cb ON a.action IN ('categorise', 'undo') AND a.before <> '' AND cb.id = CAST(a.before AS INTEGER)
		LEFT JOIN categories ca ON a.action IN ('categorise', 'undo') AND a.after <> '' AND ca.id = CAST(a.after AS INTEGER)
		WHERE ? = '' OR a.via = ?
		ORDER BY a.id DESC LIMIT ?`, string(via), string(via), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var when, undone string
		if err := rows.Scan(&e.ID, &when, &e.UserName, &e.Via, &e.TokenName, &e.Action, &e.SubjectID,
			&e.Before, &e.After, &undone, &e.Description, &e.BeforeName, &e.AfterName); err != nil {
			return nil, err
		}
		e.When, _ = time.Parse(time.RFC3339, when)
		if undone != "" {
			e.UndoneAt, _ = time.Parse(time.RFC3339, undone)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

var (
	// ErrChangedSince means the transaction no longer has the category the
	// change gave it -- someone has changed it again -- so undoing would
	// clobber the newer choice. Nothing is changed.
	ErrChangedSince  = errors.New("that transaction has been changed since; nothing was undone")
	ErrAlreadyUndone = errors.New("that change has already been undone")
)

// Undo reverses one categorisation, restoring the category it replaced.
//
// The guard is in the UPDATE: it applies only while the transaction still holds
// the category this change gave it. A later change -- by either person, or
// another AI call -- wins, and the undo is refused rather than silently
// overwriting it.
func (s *Store) Undo(id int64, a Actor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var action, before, after, undone string
	var subject sql.NullInt64
	err = tx.QueryRow(`SELECT action, subject_id, before, after, undone_at FROM audit_log WHERE id = ?`, id).
		Scan(&action, &subject, &before, &after, &undone)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	case action != "categorise":
		return fmt.Errorf("%w: only categorisations can be undone", ErrInvalid)
	case undone != "":
		return ErrAlreadyUndone
	}

	restore, current := idArg(before), idArg(after)
	source := "manual"
	if before == "" {
		source = ""
	}
	res, err := tx.Exec(`UPDATE transactions SET category_id = ?, category_source = ?
		WHERE id = ? AND category_id IS ?`, restore, source, subject.Int64, current)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrChangedSince
	}
	if _, err := tx.Exec(`UPDATE audit_log SET undone_at = ? WHERE id = ?`, formatTime(s.now()), id); err != nil {
		return err
	}
	if err := s.audit(tx, a, "undo", subject.Int64, after, before); err != nil {
		return err
	}
	return tx.Commit()
}

// idArg turns an audit value back into a query argument: ” is NULL.
func idArg(v string) any {
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return n
}

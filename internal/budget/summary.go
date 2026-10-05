package budget

import (
	"database/sql"
)

// Line is one category in a month.
type Line struct {
	CategoryID int64
	Name       string
	Kind       string
	Archived   bool
	Target     sql.NullInt64
	// TotalCents is the signed sum of the month's transactions in the category:
	// negative for spending, positive for income and refunds.
	TotalCents int64
	Count      int
}

// SpentCents is how much an expense category took: refunds reduce it, and it
// can go below zero in a month of more refunds than purchases.
func (l Line) SpentCents() int64 { return -l.TotalCents }

// RemainingCents is target minus spent for an expense category with a target.
func (l Line) RemainingCents() int64 { return l.Target.Int64 - l.SpentCents() }

// Over reports an expense category past its target.
func (l Line) Over() bool { return l.Target.Valid && l.SpentCents() > l.Target.Int64 }

// Summary is a month against its targets. It is the single source both the
// dashboard and the MCP spending_summary tool read, so the two cannot disagree.
type Summary struct {
	Month    string
	Expenses []Line
	Income   []Line
	Transfer []Line

	SpentCents    int64 // all expense categories
	TargetCents   int64 // sum of expense targets
	IncomeCents   int64 // all income categories
	TransferCents int64 // net of transfers; ~0 when both sides are imported
	UncatInCents  int64
	UncatOutCents int64
	UncatCount    int
}

// NetCents is income minus spending. Transfers and uncategorised money are left
// out: the first is not income or spending, the second is not yet known.
func (s Summary) NetCents() int64 { return s.IncomeCents - s.SpentCents }

// Summary computes one month (YYYY-MM). Every unarchived category appears, so a
// category with a target and nothing spent yet is visible; an archived one
// appears only if it has transactions that month.
func (s *Store) Summary(month string) (Summary, error) {
	sum := Summary{Month: month}
	rows, err := s.db.Query(`
		SELECT c.id, c.name, c.kind, c.archived,
		       (SELECT target_cents FROM budget_targets bt WHERE bt.category_id = c.id AND bt.month = ?),
		       COALESCE(sum(t.amount_cents), 0), count(t.id)
		FROM categories c
		LEFT JOIN transactions t ON t.category_id = c.id AND t.posted_on LIKE ?
		GROUP BY c.id
		HAVING c.archived = 0 OR count(t.id) > 0
		ORDER BY c.name COLLATE NOCASE`, month, month+"-%")
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.CategoryID, &l.Name, &l.Kind, &l.Archived, &l.Target, &l.TotalCents, &l.Count); err != nil {
			return sum, err
		}
		switch l.Kind {
		case KindExpense:
			sum.Expenses = append(sum.Expenses, l)
			sum.SpentCents += l.SpentCents()
			sum.TargetCents += l.Target.Int64
		case KindIncome:
			sum.Income = append(sum.Income, l)
			sum.IncomeCents += l.TotalCents
		case KindTransfer:
			sum.Transfer = append(sum.Transfer, l)
			sum.TransferCents += l.TotalCents
		}
	}
	if err := rows.Err(); err != nil {
		return sum, err
	}
	err = s.db.QueryRow(`
		SELECT COALESCE(sum(CASE WHEN amount_cents > 0 THEN amount_cents END), 0),
		       COALESCE(sum(CASE WHEN amount_cents < 0 THEN amount_cents END), 0),
		       count(*)
		FROM transactions WHERE category_id IS NULL AND posted_on LIKE ?`, month+"-%").
		Scan(&sum.UncatInCents, &sum.UncatOutCents, &sum.UncatCount)
	return sum, err
}

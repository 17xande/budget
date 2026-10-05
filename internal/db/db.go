// Package db opens the SQLite database and owns its schema.
package db

import (
	"database/sql"
	"fmt"
	"net/url"

	// modernc rather than mattn: it is pure Go, so the binary builds with
	// CGO_ENABLED=0 and ships on distroless/static with no libc and no shell.
	// The price is a large machine-translated dependency; see README.
	_ "modernc.org/sqlite"
)

// Every table is STRICT: SQLite otherwise applies "type affinity" and will happily
// store the string "banana" in an INTEGER column. Every column is therefore TEXT or
// INTEGER. Timestamps are RFC3339 TEXT in UTC; SQLite has no datetime type.
//
// STRICT cannot be retrofitted: CREATE TABLE IF NOT EXISTS ... STRICT silently does
// nothing to a table that already exists, and reports success. db_test.go therefore
// asserts against sqlite_master rather than trusting this DDL.
//
// There is deliberately no migration machinery while the project is greenfield: a
// schema change is `make recreate-db`. A startup ALTER against a STRICT table can
// only half-migrate (new column, old table definition), so do not add one by
// reflex. Adopt goose once there is data worth preserving.
const schema = `
-- The people who can sign in. There are two and no roles: both can see and do
-- everything, which is the point of a household budget.
CREATE TABLE IF NOT EXISTS users (
	id                   INTEGER PRIMARY KEY AUTOINCREMENT,
	email                TEXT NOT NULL UNIQUE COLLATE NOCASE,
	name                 TEXT NOT NULL,
	password_hash        TEXT NOT NULL,
	-- Set when somebody else chose this password (adding a member), so the next
	-- sign-in is forced through the change form before anything else opens.
	must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
	created_at           TEXT NOT NULL,
	last_login_at        TEXT NOT NULL DEFAULT ''
) STRICT;

-- Sessions are opaque random tokens; only their sha256 is stored, so a copy of
-- this file does not hand over live sessions.
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

-- The first-run bootstrap: a deployment with no users issues a one-time token,
-- printed to the log, which claims the first account at /setup. A single row
-- (CHECK id = 1) so a second boot conflicts instead of replacing a live token,
-- and consumed for ever once spent. Preferred over a default password (which
-- someone forgets to change) or an open first-run page (which whoever finds the
-- deployment first gets to claim).
CREATE TABLE IF NOT EXISTS setup (
	id          INTEGER PRIMARY KEY CHECK (id = 1),
	token_hash  TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	consumed_at TEXT NOT NULL DEFAULT ''
) STRICT;

-- A bank account, created the first time an import names it. ref is how every
-- FNB format identifies it: the account number, or the masked card number
-- ("400000******1234") for a credit card.
CREATE TABLE IF NOT EXISTS accounts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ref        TEXT NOT NULL UNIQUE,
	name       TEXT NOT NULL,
	kind       TEXT NOT NULL CHECK (kind IN ('cheque', 'credit', 'savings', 'loan', 'other')),
	created_at TEXT NOT NULL
) STRICT;

-- One row per statement imported, or per file that failed to import, so the
-- history shows what happened to every upload and every daily fetch.
CREATE TABLE IF NOT EXISTS imports (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	source     TEXT NOT NULL CHECK (source IN ('upload', 'fnb')),
	filename   TEXT NOT NULL,
	format     TEXT NOT NULL DEFAULT '',
	account_id INTEGER REFERENCES accounts(id),
	user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	inserted   INTEGER NOT NULL DEFAULT 0,
	skipped    INTEGER NOT NULL DEFAULT 0,
	error      TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
) STRICT;

-- What a transaction is for. kind decides how reports treat it: expense counts
-- as spending (a refund in an expense category reduces it), income as income,
-- and transfer -- money moved between your own accounts, a card repayment --
-- as neither. Categories are archived, never deleted, so history keeps meaning.
CREATE TABLE IF NOT EXISTS categories (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	kind       TEXT NOT NULL CHECK (kind IN ('expense', 'income', 'transfer')),
	archived   INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
	created_at TEXT NOT NULL
) STRICT;

-- posted_on is a calendar date (YYYY-MM-DD), not an instant: FNB's dates are
-- South African days and are never converted between timezones. Money is integer
-- cents.
--
-- dedup_key makes importing idempotent across formats and exports: see
-- ledger.DedupKeys. Never FITID, which collides for identical same-day purchases
-- and differs between FNB's two OFX exports.
CREATE TABLE IF NOT EXISTS transactions (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id    INTEGER NOT NULL REFERENCES accounts(id),
	posted_on     TEXT NOT NULL CHECK (posted_on GLOB '[0-9][0-9][0-9][0-9]-[01][0-9]-[0-3][0-9]'),
	amount_cents  INTEGER NOT NULL,
	description   TEXT NOT NULL,
	balance_cents INTEGER,
	fitid         TEXT NOT NULL DEFAULT '',
	import_id     INTEGER NOT NULL REFERENCES imports(id),
	dedup_key     TEXT NOT NULL UNIQUE,
	-- category_source says who chose the category: a rule may fill an empty
	-- one, but never overrides a person's (or an AI's) choice.
	category_id     INTEGER REFERENCES categories(id),
	category_source TEXT NOT NULL DEFAULT '' CHECK (category_source IN ('', 'rule', 'manual', 'ai')),
	created_at    TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_transactions_date ON transactions(posted_on);
CREATE INDEX IF NOT EXISTS idx_transactions_account_date ON transactions(account_id, posted_on);
CREATE INDEX IF NOT EXISTS idx_transactions_category ON transactions(category_id);

-- "Description contains pattern" (case-insensitive), optionally only on one
-- account. When several match, the longest pattern wins: it is the more
-- specific one ("WOOLWORTHS FOOD" before "WOOLWORTHS").
CREATE TABLE IF NOT EXISTS rules (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	pattern     TEXT NOT NULL CHECK (length(trim(pattern)) > 0),
	category_id INTEGER NOT NULL REFERENCES categories(id),
	account_id  INTEGER REFERENCES accounts(id),
	created_at  TEXT NOT NULL
) STRICT;

-- How much a category may take in a month (or, for income, is expected to bring).
CREATE TABLE IF NOT EXISTS budget_targets (
	category_id  INTEGER NOT NULL REFERENCES categories(id),
	month        TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[01][0-9]'),
	target_cents INTEGER NOT NULL CHECK (target_cents >= 0),
	PRIMARY KEY (category_id, month)
) STRICT;

-- Every change a person or an AI makes to budgeting data, with what it replaced,
-- so a change is visible and -- for categorisation -- undoable. before/after hold
-- the old and new values as text ('' for none).
CREATE TABLE IF NOT EXISTS audit_log (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	via        TEXT NOT NULL CHECK (via IN ('web', 'mcp')),
	token_name TEXT NOT NULL DEFAULT '',
	action     TEXT NOT NULL,
	subject_id INTEGER,
	before     TEXT NOT NULL DEFAULT '',
	after      TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	undone_at  TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
`

// requiredColumns are columns added to a table after it first shipped. CREATE
// TABLE IF NOT EXISTS cannot add them to an existing file, so Open checks for
// them and refuses to start on a database that predates them -- with a message
// that says what to do -- rather than failing on the first query that needs one.
var requiredColumns = map[string][]string{
	"transactions": {"category_id", "category_source"},
}

// Open opens the database at path (creating it if needed) and applies the schema.
// IF NOT EXISTS throughout, so it is safe on every start.
func Open(path string) (*sql.DB, error) {
	q := url.Values{}
	// SQLite enforces no foreign key without this: every REFERENCES above is
	// decoration until it is set, per connection.
	q.Add("_pragma", "foreign_keys(1)")
	// modernc does not default a busy timeout the way mattn does (5s). Without
	// one a contended lock is an immediate SQLITE_BUSY rather than a short wait,
	// and with the daily fetch writing while someone browses, that would surface
	// as a random 500.
	q.Add("_pragma", "busy_timeout(5000)")
	// BEGIN IMMEDIATE takes the write lock up front. A deferred transaction that
	// reads and then writes (claiming the setup token, say) can otherwise fail
	// with SQLITE_BUSY at the upgrade, which busy_timeout does not help with.
	q.Add("_txlock", "immediate")

	d, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if _, err := d.Exec(schema); err != nil {
		d.Close()
		return nil, fmt.Errorf("apply schema: %w (a database older than the schema needs `make recreate-db`)", err)
	}
	for table, cols := range requiredColumns {
		for _, col := range cols {
			var n int
			if err := d.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
				d.Close()
				return nil, fmt.Errorf("check schema: %w", err)
			}
			if n == 0 {
				d.Close()
				return nil, fmt.Errorf("database %s predates column %s.%s; it cannot be migrated in place "+
					"(there are no migrations while the project is greenfield): recreate it with `make recreate-db` "+
					"and import the FNB exports again", path, table, col)
			}
		}
	}
	return d, nil
}

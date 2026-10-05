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
`

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
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return d, nil
}

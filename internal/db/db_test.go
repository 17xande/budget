package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// The DDL can claim STRICT and still not be: CREATE TABLE IF NOT EXISTS skips a
// table that already exists. sqlite_master is what the file actually holds.
func TestEveryTableIsStrict(t *testing.T) {
	d := openTemp(t)
	rows, err := d.Query(`SELECT name, sql FROM sqlite_master
	                      WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		seen++
		if !strings.Contains(strings.ToUpper(ddl), ") STRICT") {
			t.Errorf("table %s is not STRICT", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Without a floor this passes vacuously the day the schema stops applying.
	if seen < 6 {
		t.Errorf("found %d tables, want at least 6", seen)
	}
}

func TestStrictRefusesWrongType(t *testing.T) {
	d := openTemp(t)
	_, err := d.Exec(`INSERT INTO users (email, name, password_hash, must_change_password, created_at)
	                  VALUES ('a@b.c', 'A', 'h', 'banana', '2026-01-01T00:00:00Z')`)
	if err == nil {
		t.Fatal("STRICT accepted text in an INTEGER column")
	}
}

// foreign_keys is a per-connection pragma that defaults off, so it has to be in
// the DSN -- and this is the test that notices if it ever is not.
func TestForeignKeysEnforced(t *testing.T) {
	d := openTemp(t)
	_, err := d.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at)
	                  VALUES ('x', 999, '', '')`)
	if err == nil {
		t.Fatal("a session for a nonexistent user was accepted: foreign keys are off")
	}
}

func TestBoolRoundTrips(t *testing.T) {
	d := openTemp(t)
	if _, err := d.Exec(`INSERT INTO users (email, name, password_hash, must_change_password, created_at)
	                     VALUES (?, ?, ?, ?, ?)`, "a@b.c", "A", "h", true, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := d.QueryRow(`SELECT must_change_password FROM users`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("bool true did not round-trip")
	}
}

func TestOpenIsRepeatable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "again.db")
	for range 2 {
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		d.Close()
	}
}

package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBackup(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO accounts (ref, name, kind, created_at) VALUES ('x1', 'Cheque', 'cheque', '2026-10-05T00:00:00Z')`)
	backups := filepath.Join(dir, "backups")
	ctx := context.Background()

	path, made, err := Backup(ctx, d, backups, "2026-10-05", 3)
	if err != nil || !made {
		t.Fatalf("first backup: made=%v err=%v", made, err)
	}
	// The copy is a whole database: it opens through the same schema checks
	// and holds the data.
	cp, err := Open(path)
	if err != nil {
		t.Fatalf("the backup does not open: %v", err)
	}
	var n int
	cp.QueryRow(`SELECT count(*) FROM accounts`).Scan(&n)
	cp.Close()
	if n != 1 {
		t.Errorf("backup holds %d accounts", n)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v, want 0600", info.Mode().Perm())
	}

	// Once a day: a second call the same day writes nothing.
	if _, made, err := Backup(ctx, d, backups, "2026-10-05", 3); err != nil || made {
		t.Errorf("same-day backup: made=%v err=%v", made, err)
	}

	// Retention keeps the newest, and leaves files that are not backups alone.
	os.WriteFile(filepath.Join(backups, "notes.txt"), []byte("mine"), 0o600)
	os.WriteFile(filepath.Join(backups, ".budget-2026-10-09.db.tmp"), []byte("torn"), 0o600)
	for _, day := range []string{"2026-10-06", "2026-10-07", "2026-10-08", "2026-10-09"} {
		if _, _, err := Backup(ctx, d, backups, day, 3); err != nil {
			t.Fatalf("%s: %v", day, err)
		}
	}
	files, _ := Backups(backups)
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	want := []string{"budget-2026-10-09.db", "budget-2026-10-08.db", "budget-2026-10-07.db"}
	if len(names) != 3 || names[0] != want[0] || names[2] != want[2] {
		t.Errorf("kept %v, want %v", names, want)
	}
	if _, err := os.Stat(filepath.Join(backups, "notes.txt")); err != nil {
		t.Error("pruning deleted a file that is not a backup")
	}
	if _, err := os.Stat(filepath.Join(backups, ".budget-2026-10-09.db.tmp")); err == nil {
		t.Error("a leftover temporary file survived")
	}

	if _, _, err := Backup(ctx, d, backups, "../escape", 3); err == nil {
		t.Error("a malformed day was accepted")
	}
}

func TestBackupsMissingDir(t *testing.T) {
	if files, err := Backups(filepath.Join(t.TempDir(), "none")); err != nil || files != nil {
		t.Errorf("missing dir: %v %v", files, err)
	}
}

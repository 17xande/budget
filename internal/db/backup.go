package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A backup is budget-YYYY-MM-DD.db. Anything else in the directory is left alone,
// so pointing BACKUP_DIR at a shared folder can never prune someone else's files.
var backupName = regexp.MustCompile(`^budget-\d{4}-\d{2}-\d{2}\.db$`)

// BackupFile is one backup on disk.
type BackupFile struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Backup writes a consistent copy of the database to dir/budget-<day>.db, unless
// that day's backup already exists, then deletes all but the newest keep. day is
// YYYY-MM-DD; made reports whether a file was written.
//
// VACUUM INTO rather than copying the file: a copy taken while a write is in
// flight can be torn, and VACUUM INTO reads inside one transaction, so the copy is
// exactly the database at one instant, compacted, and openable on its own. It is
// written under a temporary name and renamed, so a crash mid-backup never leaves a
// half-written file that looks like the day's backup and blocks the real one.
func Backup(ctx context.Context, d *sql.DB, dir, day string, keep int) (path string, made bool, err error) {
	path = filepath.Join(dir, "budget-"+day+".db")
	if !backupName.MatchString(filepath.Base(path)) {
		return "", false, fmt.Errorf("backup: bad day %q", day)
	}
	if _, err := os.Stat(path); err == nil {
		return path, false, prune(dir, keep)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("backup: %w", err)
	}
	tmp := filepath.Join(dir, ".budget-"+day+".db.tmp")
	// VACUUM INTO refuses an existing file; a leftover is from a crashed attempt.
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("backup: %w", err)
	}
	if _, err := d.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return "", false, fmt.Errorf("backup: %w", err)
	}
	// The copy holds both people's financial history: owner-only, like the
	// database itself should be.
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return "", false, fmt.Errorf("backup: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", false, fmt.Errorf("backup: %w", err)
	}
	return path, true, prune(dir, keep)
}

// Backups lists the backups in dir, newest first. A missing directory is no
// backups, not an error.
func Backups(dir string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []BackupFile
	for _, e := range entries {
		if !e.Type().IsRegular() || !backupName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, BackupFile{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	// The date is in the name, so name order is date order.
	slices.SortFunc(out, func(a, b BackupFile) int { return strings.Compare(b.Name, a.Name) })
	return out, nil
}

func prune(dir string, keep int) error {
	files, err := Backups(dir)
	if err != nil {
		return fmt.Errorf("backup: prune: %w", err)
	}
	if keep < 1 {
		keep = 1 // never delete the backup just made
	}
	for _, f := range files[min(keep, len(files)):] {
		if err := os.Remove(filepath.Join(dir, f.Name)); err != nil {
			return fmt.Errorf("backup: prune: %w", err)
		}
	}
	return nil
}

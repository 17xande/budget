package handler_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/budget/internal/handler"
)

// A backup that silently stopped would only be noticed when it was needed, so the
// settings page says when there is none, and when the newest is stale.
func TestSettingsBackupStatus(t *testing.T) {
	if page := newEnv(t).claim().get("/settings").body; !strings.Contains(page, "Backups are off") {
		t.Error("with no backup dir, the page does not say backups are off")
	}

	dir := t.TempDir()
	owner := newEnvWith(t, handler.Config{SessionTTL: time.Hour, BackupDir: dir}).claim()
	if page := owner.get("/settings").body; !strings.Contains(page, "No backup has been made yet") {
		t.Error("an empty backup dir is not reported")
	}

	old := filepath.Join(dir, "budget-2026-09-01.db")
	os.WriteFile(old, []byte("x"), 0o600)
	os.Chtimes(old, time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour))
	if page := owner.get("/settings").body; !strings.Contains(page, "backups are failing") {
		t.Error("a three-day-old newest backup is not flagged")
	}

	os.WriteFile(filepath.Join(dir, "budget-2026-10-05.db"), []byte("x"), 0o600)
	page := owner.get("/settings").body
	if !strings.Contains(page, "budget-2026-10-05.db") || !strings.Contains(page, "2 kept") || strings.Contains(page, "failing") {
		t.Error("a fresh backup is not shown as the latest, healthy one")
	}
}

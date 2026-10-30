package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/17xande-dev/budget/internal/db"
)

var sast = mustLoadLocation("Africa/Johannesburg")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // time/tzdata is embedded, so this cannot happen
	}
	return loc
}

// backupDaily makes the day's backup at startup and then checks hourly, so a
// server that was down at midnight still backs up the day it comes back. Backup
// itself skips a day already done, which makes the hourly check nearly free.
func backupDaily(ctx context.Context, d *sql.DB, dir string, keep int, every time.Duration) {
	run := func(now time.Time) {
		path, made, err := db.Backup(ctx, d, dir, now.In(sast).Format("2006-01-02"), keep)
		switch {
		case err != nil:
			// Logged, and visible on the settings page as a backup going stale.
			log.Printf("backup: %v", err)
		case made:
			log.Printf("backup: wrote %s", path)
		}
	}
	run(time.Now())
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			run(now)
		}
	}
}

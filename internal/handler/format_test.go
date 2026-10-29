package handler

import (
	"database/sql"
	"testing"

	"github.com/17xande/budget/internal/budget"
)

func TestFormatMoney(t *testing.T) {
	for cents, want := range map[int64]string{
		0:          "R 0.00",
		5:          "R 0.05",
		8990:       "R 89.90",
		-8990:      "−R 89.90",
		100000:     "R 1 000.00",
		-123456789: "−R 1 234 567.89",
		99999999:   "R 999 999.99",
	} {
		if got := formatMoney(cents); got != want {
			t.Errorf("formatMoney(%d) = %q, want %q", cents, got, want)
		}
	}
}

func TestLabels(t *testing.T) {
	if got := monthLabel("2026-10"); got != "October 2026" {
		t.Errorf("monthLabel = %q", got)
	}
	if got := dateLabel("2026-10-05"); got != "Mon 5 Oct 2026" {
		t.Errorf("dateLabel = %q", got)
	}
	if got := monthLabel("nonsense"); got != "nonsense" {
		t.Errorf("monthLabel passthrough = %q", got)
	}
}

func TestFormatRand(t *testing.T) {
	for cents, want := range map[int64]string{
		0:         "R0",
		49:        "R0",
		50:        "R1",
		-49:       "R0",
		-50:       "−R1",
		1450000:   "R14 500",
		-1450000:  "−R14 500",
		123456789: "R1 234 568",
		-2980:     "−R30",
		149:       "R1",
		150:       "R2",
	} {
		if got := formatRand(cents); got != want {
			t.Errorf("formatRand(%d) = %q, want %q", cents, got, want)
		}
	}
}

func TestLineState(t *testing.T) {
	line := func(target, spent int64, hasTarget bool) budget.Line {
		return budget.Line{Target: sql.NullInt64{Int64: target, Valid: hasTarget}, TotalCents: -spent}
	}
	for name, tc := range map[string]struct {
		l    budget.Line
		want string
	}{
		"no target":           {line(0, 5000, false), ""},
		"zero spent":          {line(10000, 0, true), "ok"},
		"89.9 percent":        {line(10000, 8999, true), "ok"},
		"90 percent":          {line(10000, 9000, true), "near"},
		"exactly the target":  {line(10000, 10000, true), "near"},
		"over":                {line(10000, 10001, true), "over"},
		"refunds":             {line(10000, -500, true), "ok"},
		"zero target, zero":   {line(0, 0, true), "ok"},
		"zero target, spent":  {line(0, 1, true), "over"},
		"zero target, refund": {line(0, -500, true), "ok"},
	} {
		if got := lineState(tc.l); got != tc.want {
			t.Errorf("%s: lineState = %q, want %q", name, got, tc.want)
		}
	}
}

func TestFormatOver(t *testing.T) {
	for cents, want := range map[int64]string{-1: "R1", -49: "R1", -100: "R1", -2980: "R30", 1: "R1", 0: "R0", -1450000: "R14\u00a0500"} {
		if got := formatOver(cents); got != want {
			t.Errorf("formatOver(%d) = %q, want %q", cents, got, want)
		}
	}
}

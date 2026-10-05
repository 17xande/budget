package handler

import "testing"

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

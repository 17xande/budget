package handler

import (
	"strconv"
	"strings"
	"time"
)

// formatMoney renders cents as South Africans write rand: "R 1 234.56", grouped
// with non-breaking spaces so an amount never wraps across a line, and a real
// minus sign ("−R 89.90") rather than a hyphen.
func formatMoney(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "−"
		cents = -cents
	}
	whole := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(r)
	}
	frac := cents % 100
	return sign + "R " + b.String() + "." + string(rune('0'+frac/10)) + string(rune('0'+frac%10))
}

// moneyClass is the CSS class for an amount: money out and money in read
// differently at a glance.
func moneyClass(cents int64) string {
	switch {
	case cents < 0:
		return "num out"
	case cents > 0:
		return "num in"
	default:
		return "num"
	}
}

// monthLabel renders "2026-10" as "October 2026"; anything else passes through.
func monthLabel(ym string) string {
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return ym
	}
	return t.Format("January 2006")
}

// dateLabel renders "2026-10-05" as "Mon 5 Oct 2026". It is a calendar date, so
// there is no timezone to get wrong.
func dateLabel(ymd string) string {
	t, err := time.Parse("2006-01-02", ymd)
	if err != nil {
		return ymd
	}
	return t.Format("Mon 2 Jan 2006")
}

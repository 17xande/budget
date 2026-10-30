package handler

import (
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/budget"
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

// formatRand renders cents as whole rand for the overview: "R14 500", "−R30".
// Display only: it rounds half away from zero, and the stored and summed values
// stay in cents, so rounded parts can differ from a rounded total by R1.
func formatRand(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "−"
		cents = -cents
	}
	whole := strconv.FormatInt((cents+50)/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString("\u00a0")
		}
		b.WriteRune(r)
	}
	if b.String() == "0" {
		sign = ""
	}
	return sign + "R" + b.String()
}

// formatOver renders how far over a target a line is, always at least R1: an
// overage of a few cents is still over, and "R0 over" would contradict the row.
func formatOver(cents int64) string {
	if cents < 0 {
		cents = -cents
	}
	if cents > 0 && cents < 100 {
		cents = 100
	}
	return formatRand(cents)
}

// lineState is a category's status for the overview: "" with no target, "over"
// past the target, "near" from 90% of it, else "ok". It only compares figures
// the Line already has.
func lineState(l budget.Line) string {
	switch {
	case !l.Target.Valid:
		return ""
	case l.Over():
		return "over"
	case l.Target.Int64 == 0:
		return "ok" // nothing spent against a zero target (spent > 0 is over, above)
	case l.SpentCents()*10 >= l.Target.Int64*9:
		return "near"
	}
	return "ok"
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

// Package money parses and formats rand amounts as integer cents. Never a float:
// 0.1 + 0.2 is not 0.3 in binary, and money that drifts by a cent is money that
// does not reconcile.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseCents reads a decimal amount ("-1234.56", "12.5", "100") into cents
// exactly. Thousands separators are not accepted: an ambiguous "1,000" is an
// error rather than a guess.
func ParseCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	orig := s
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && (frac == "" || len(frac) > 2)) {
		return 0, fmt.Errorf("not an amount: %q", orig)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	var n int64
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an amount: %q", orig)
		}
		if n > (1<<62)/10 {
			return 0, fmt.Errorf("amount out of range: %q", orig)
		}
		n = n*10 + int64(r-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

// Decimal renders cents as a plain machine-readable decimal ("-89.90"), the form
// an API returns. Display formatting ("−R 89.90") is the handler's job.
func Decimal(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%s.%02d", sign, strconv.FormatInt(cents/100, 10), cents%100)
}

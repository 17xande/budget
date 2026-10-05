package importer

import (
	"errors"
	"fmt"
	"strings"
)

// windows1252 is what bytes 0x80-0x9F mean in Windows-1252, the charset FNB's OFX
// header declares (CHARSET:1252). Everywhere else 1252 agrees with Latin-1, whose
// bytes are their own code points. Undefined positions stay as their C1 code
// point, which is what a lenient decoder does.
var windows1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

// decodeWindows1252 converts FNB's bytes to a Go string. Written out rather than
// taken from golang.org/x/text: it is a 32-entry table, and the alternative is a
// module for one charset.
func decodeWindows1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(windows1252[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// parseCents reads a decimal amount ("-1234.56", "12.5", "100") into cents
// exactly. Never through a float: 0.1 + 0.2 is not 0.3 in binary, and money that
// drifts by a cent is money that does not reconcile.
func parseCents(s string) (int64, error) {
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

var errBadDate = errors.New("bad date")

// isoDate turns FNB's dates -- "2026/10/05" (CSV), "20261005" (OFX, possibly with
// a time after it), "2026-10-05" (advanced OFX) -- into YYYY-MM-DD.
func isoDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	var y, m, d string
	switch {
	case len(s) >= 10 && (s[4] == '/' || s[4] == '-') && s[7] == s[4]:
		y, m, d = s[0:4], s[5:7], s[8:10]
	case len(s) >= 8:
		y, m, d = s[0:4], s[4:6], s[6:8]
	default:
		return "", fmt.Errorf("%w: %q", errBadDate, s)
	}
	for _, part := range []string{y, m, d} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("%w: %q", errBadDate, s)
			}
		}
	}
	if m < "01" || m > "12" || d < "01" || d > "31" {
		return "", fmt.Errorf("%w: %q", errBadDate, s)
	}
	return y + "-" + m + "-" + d, nil
}

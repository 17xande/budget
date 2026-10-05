package money

import "testing"

func TestParseCents(t *testing.T) {
	good := map[string]int64{
		"0": 0, "12": 1200, "12.5": 1250, "12.05": 1205, "-1234.56": -123456, "+3.10": 310, " 7.00 ": 700,
		// The classic float trap: 0.29 * 100 is 28.999999999999996.
		"0.29": 29,
	}
	for in, want := range good {
		if got, err := ParseCents(in); err != nil || got != want {
			t.Errorf("ParseCents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-", "1.234", "1.", ".5", "1,000.00", "abc", "1e3", "99999999999999999999"} {
		if _, err := ParseCents(in); err == nil {
			t.Errorf("ParseCents(%q) accepted", in)
		}
	}
}

func TestDecimalRoundTrips(t *testing.T) {
	for _, c := range []int64{0, 5, -5, 8990, -8990, 123456789, -100} {
		got, err := ParseCents(Decimal(c))
		if err != nil || got != c {
			t.Errorf("Decimal(%d) = %q, parses back to %d (%v)", c, Decimal(c), got, err)
		}
	}
	if Decimal(-8990) != "-89.90" {
		t.Errorf("Decimal(-8990) = %q", Decimal(-8990))
	}
}

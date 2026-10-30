package middleware

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestIPKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.5":          "203.0.113.5",
		"::ffff:203.0.113.5":   "203.0.113.5",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"fe80::1%eth0":         "fe80::/64",
		"not-an-ip":            "not-an-ip",
		"2001:db8:1:3:3:4:5:6": "2001:db8:1:3::/64",
	} {
		if got := IPKey(in); got != want {
			t.Errorf("IPKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLimiterCapFailsClosed(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := &Limiter{buckets: map[string]*bucket{}, r: rate.Every(time.Hour), b: 5, max: 3,
		now: func() time.Time { return now }}

	for _, k := range []string{"a", "b", "c"} {
		if !l.Allow(k) {
			t.Fatalf("Allow(%q) refused below the cap", k)
		}
	}
	if l.Allow("d") {
		t.Error("a new key was admitted at the cap")
	}
	if len(l.buckets) != 3 {
		t.Errorf("buckets = %d, want 3", len(l.buckets))
	}
	// Known keys are unaffected.
	if !l.Allow("a") {
		t.Error("an existing key was refused at the cap")
	}

	// Within capSweepEvery nothing is swept, even though the buckets are idle...
	now = now.Add(idleTTL + time.Minute)
	l.capSwept = now.Add(-time.Second)
	if l.Allow("d") {
		t.Error("swept again inside capSweepEvery")
	}
	// ...and after it the idle ones are dropped and the newcomer fits.
	now = now.Add(capSweepEvery)
	if !l.Allow("d") {
		t.Error("idle buckets were not swept to make room")
	}
}

func TestExhaustedDoesNotCreateBuckets(t *testing.T) {
	l := NewLimiter(rate.Every(time.Hour), 1)
	for _, k := range []string{"email:a", "email:b", "email:c"} {
		if l.Exhausted(k) {
			t.Errorf("unseen key %q reported exhausted", k)
		}
	}
	if len(l.buckets) != 0 {
		t.Errorf("Exhausted created %d bucket(s)", len(l.buckets))
	}
}

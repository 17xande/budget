package middleware

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter is a token bucket per key -- a client IP, or the email an attempt names.
//
// Every endpoint that verifies a secret goes through one: the login, the setup
// claim and the own-password change. The last is easy to forget and matters as
// much as the login: it is a guessing oracle for the current password, and every
// attempt costs a full argon2 verification (~64 MiB), so an unmetered one is a
// memory lever as well as a guessing one.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	r       rate.Limit
	b       int
	now     func() time.Time

	// max caps len(buckets); a field, not only the constant, so a test can lower
	// it instead of creating ten thousand entries.
	max int
	// capSwept is when a cap-triggered sweep last ran; see capSweepEvery.
	capSwept time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// maxBuckets is the most buckets one limiter holds. Keying by /64 stops a single
// IPv6 prefix minting buckets, but an attacker can still cycle through prefixes,
// IPv4 addresses or attacker-chosen emails, and the periodic sweep only drops
// buckets idle for idleTTL. The cap is what makes the map's size a bound.
const maxBuckets = 10_000

// idleTTL is how long a bucket may sit unused before the sweep drops it: long
// enough to have refilled, so forgetting it costs nothing.
const idleTTL = 30 * time.Minute

// capSweepEvery is the least time between sweeps triggered by hitting the cap. A
// sweep scans every bucket under the lock; at the cap every refused newcomer
// would otherwise run one, and an attacker's request rate would become the
// lock's busy time.
const capSweepEvery = 5 * time.Second

// IPKey is the identity a client address is throttled under: the address for
// IPv4, the /64 for IPv6. A /64 is the smallest block an ISP hands one
// subscriber, so one prefix holds 2^64 addresses and keying on the full address
// would give each a fresh bucket. An IPv4-mapped address (::ffff:a.b.c.d) is
// unmapped to share its bucket with the plain form, and an IPv6 zone is dropped.
// Anything unparseable keeps its raw string.
func IPKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String()
}

// NewLimiter returns a limiter allowing burst attempts per key, refilling at r.
func NewLimiter(r rate.Limit, burst int) *Limiter {
	l := &Limiter{buckets: make(map[string]*bucket), r: r, b: burst, now: time.Now, max: maxBuckets}
	go l.sweep()
	return l
}

// get returns key's bucket, creating it if create is set. It reports false when
// there is none: absent and not created, or the map is full of live clients.
func (l *Limiter) get(key string, create bool) (*rate.Limiter, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if bk, ok := l.buckets[key]; ok {
		bk.lastSeen = now
		return bk.limiter, true
	}
	if !create {
		return nil, false
	}
	if len(l.buckets) >= l.max && (l.capSwept.IsZero() || now.Sub(l.capSwept) >= capSweepEvery) {
		l.evict(now)
		l.capSwept = now
	}
	if len(l.buckets) >= l.max {
		return nil, false
	}
	lim := rate.NewLimiter(l.r, l.b)
	l.buckets[key] = &bucket{limiter: lim, lastSeen: now}
	return lim, true
}

// Allow spends a token for key, reporting whether one was available. It fails
// closed: when the map is full of live buckets a new key is refused, because
// failing open would let a flood of fresh keys switch the limiter off.
func (l *Limiter) Allow(key string) bool {
	lim, ok := l.get(key, true)
	return ok && lim.AllowN(l.now(), 1)
}

// Exhausted reports whether key has no token left, without spending one. Paired
// with Allow, it lets a caller charge only for failures: see the login handler,
// which must not lock you out for signing in successfully a few times.
//
// A key that has never been charged is not exhausted, and asking does not create
// a bucket: otherwise an attacker naming a fresh email on every request would
// mint a bucket each time, for free.
func (l *Limiter) Exhausted(key string) bool {
	lim, ok := l.get(key, false)
	return ok && lim.TokensAt(l.now()) < 1
}

// evict drops buckets idle long enough to have refilled. The caller holds l.mu.
func (l *Limiter) evict(now time.Time) {
	for k, bk := range l.buckets {
		if now.Sub(bk.lastSeen) > idleTTL {
			delete(l.buckets, k)
		}
	}
}

// sweep drops idle buckets periodically, so the map shrinks again after a burst
// of distinct keys.
func (l *Limiter) sweep() {
	for range time.Tick(5 * time.Minute) {
		l.mu.Lock()
		l.evict(l.now())
		l.mu.Unlock()
	}
}

// ByIP wraps a handler so each client IP spends a token per request. source says
// where ClientIP reads the address from.
func (l *Limiter) ByIP(source ClientIPSource) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow("ip:" + IPKey(ClientIP(r, source))) {
				http.Error(w, "Too many attempts. Please wait a minute and try again.", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

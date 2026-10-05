package middleware

import (
	"net"
	"net/http"
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
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewLimiter returns a limiter allowing burst attempts per key, refilling at r.
func NewLimiter(r rate.Limit, burst int) *Limiter {
	l := &Limiter{buckets: make(map[string]*bucket), r: r, b: burst, now: time.Now}
	go l.sweep()
	return l
}

func (l *Limiter) get(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bk, ok := l.buckets[key]; ok {
		bk.lastSeen = l.now()
		return bk.limiter
	}
	lim := rate.NewLimiter(l.r, l.b)
	l.buckets[key] = &bucket{limiter: lim, lastSeen: l.now()}
	return lim
}

// Allow spends a token for key, reporting whether one was available.
func (l *Limiter) Allow(key string) bool {
	return l.get(key).AllowN(l.now(), 1)
}

// Exhausted reports whether key has no token left, without spending one. Paired
// with Allow, it lets a caller charge only for failures: see the login handler,
// which must not lock you out for signing in successfully a few times.
func (l *Limiter) Exhausted(key string) bool {
	return l.get(key).TokensAt(l.now()) < 1
}

// sweep drops buckets idle long enough to have refilled, so the map cannot grow
// without bound under a stream of distinct keys.
func (l *Limiter) sweep() {
	for range time.Tick(5 * time.Minute) {
		l.mu.Lock()
		for k, bk := range l.buckets {
			if l.now().Sub(bk.lastSeen) > 30*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.mu.Unlock()
	}
}

// ByIP wraps a handler so each client IP spends a token per request. trusted is
// the proxy list ClientIP honours forwarding headers from.
func (l *Limiter) ByIP(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow("ip:" + ClientIP(r, trusted)) {
				http.Error(w, "Too many attempts. Please wait a minute and try again.", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

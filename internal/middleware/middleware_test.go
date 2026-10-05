package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/17xande/budget/internal/middleware"
	"golang.org/x/time/rate"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

func TestSecurityHeaders(t *testing.T) {
	h := middleware.SecurityHeaders(http.NotFoundHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/no-such-page", nil))
	// Applies to a 404 too: the headers are set outside the routing.
	if got := rec.Header().Get("Content-Security-Policy"); got != middleware.CSP {
		t.Errorf("CSP = %q", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a page response is cacheable")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/static/styles.css", nil))
	if rec.Header().Get("Cache-Control") == "no-store" {
		t.Error("static assets marked no-store: they are content-addressed and should cache")
	}
}

// Pin the policy, so loosening it is a deliberate edit to this test and not a side
// effect of some other change.
func TestCSPHasNoUnsafeInline(t *testing.T) {
	want := "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; " +
		"img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
	if middleware.CSP != want {
		t.Errorf("CSP changed:\n got %s\nwant %s", middleware.CSP, want)
	}
}

func TestLimiterByIP(t *testing.T) {
	l := middleware.NewLimiter(rate.Every(time.Hour), 2)
	h := l.ByIP(nil)(ok)
	codes := []int{}
	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want 200 200 429", codes)
	}

	// Another address has its own bucket.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "198.51.100.1:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("a different IP was limited: %d", rec.Code)
	}
}

func TestLimiterExhaustedDoesNotSpend(t *testing.T) {
	l := middleware.NewLimiter(rate.Every(time.Hour), 1)
	for range 5 {
		if l.Exhausted("k") {
			t.Fatal("peeking spent a token")
		}
	}
	l.Allow("k")
	if !l.Exhausted("k") {
		t.Fatal("not exhausted after spending the only token")
	}
}

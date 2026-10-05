package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The body limit has to act before nosurf parses the form, and must answer a
// declared-oversized body with 413 rather than letting it through to become a
// confusing CSRF failure.
func TestLimitBodies(t *testing.T) {
	reached := false
	h := limitBodies(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))

	cases := []struct {
		method, path string
		length       int64
		want         int
	}{
		{"POST", "/import", MaxUploadBytes, 200},
		{"POST", "/import", MaxUploadBytes + 1, http.StatusRequestEntityTooLarge},
		// Only the import gets the large allowance.
		{"POST", "/members", maxFormBytes + 1, http.StatusRequestEntityTooLarge},
		{"POST", "/members", 100, 200},
	}
	for _, c := range cases {
		reached = false
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(""))
		req.ContentLength = c.length
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s length %d: %d, want %d", c.method, c.path, c.length, rec.Code, c.want)
		}
		if reached != (c.want == 200) {
			t.Errorf("%s %s length %d: reached handler = %v", c.method, c.path, c.length, reached)
		}
	}
}

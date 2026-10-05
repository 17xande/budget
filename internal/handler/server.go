package handler

import (
	"log"
	"net/http"

	"github.com/17xande-dev/budget/internal/middleware"
	"github.com/justinas/nosurf"
)

// Server composes the whole HTTP stack: routes, CSRF, security headers. It lives
// here rather than in main so the tests exercise exactly the stack production
// runs -- the order of these layers is itself something worth testing.
//
// protect is RequireUser; limit is the per-IP limiter for secret-verifying routes.
// mcpHandler, when not nil, is the MCP endpoint (internal/mcpserver).
func (h *Handler) Server(protect, limit func(http.Handler) http.Handler, mcpHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	h.Register(mux, protect, limit)

	csrf := nosurf.New(mux)
	csrf.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("csrf refused %s %s: %v", r.Method, r.URL.Path, nosurf.Reason(r))
		http.Error(w, "The form expired or came from another site. Go back, reload the page and try again.",
			http.StatusForbidden)
	}))
	// nosurf compares a POST's Origin against the request's own host and assumes
	// https when doing so. Behind Traefik the connection reaching us is plain http
	// while the browser's origin is https, so CookieSecure -- "this deployment is
	// https" -- is the right signal rather than r.TLS. Without this, plain-http
	// local development would 403 every form.
	csrf.SetIsTLSFunc(func(*http.Request) bool { return h.cookieSecure })
	csrf.SetBaseCookie(http.Cookie{Path: "/", HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode})

	// The MCP endpoint sits beside the CSRF layer, not inside it: it is
	// authenticated by a bearer token alone, never a cookie, so there is no
	// ambient credential for a forged request to ride on -- and nosurf would
	// refuse every MCP client, which sends no CSRF token. A test pins these two
	// lines, so nothing else gets mounted outside nosurf by accident.
	root := http.NewServeMux()
	if mcpHandler != nil {
		root.Handle("/mcp", mcpHandler)
	}
	root.Handle("/", csrf)

	// Security headers outermost, so they are on every response: a CSRF refusal
	// and a 404 included. The body limit sits outside the CSRF layer because
	// nosurf reads the form -- a whole multipart upload -- before any handler
	// runs, so a limit inside a handler would come too late.
	return middleware.SecurityHeaders(limitBodies(root))
}

// maxFormBytes bounds every request body except an import upload. The forms are
// a few fields; nothing legitimate comes close.
const maxFormBytes = 1 << 20

// limitBodies refuses an oversized body up front when it declares its length,
// and caps it as it is read when it does not.
func limitBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxFormBytes)
		if r.Method == http.MethodPost && r.URL.Path == "/import" {
			limit = MaxUploadBytes
		}
		if r.ContentLength > limit {
			http.Error(w, "That upload is too large.", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

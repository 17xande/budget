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
func (h *Handler) Server(protect, limit func(http.Handler) http.Handler) http.Handler {
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

	// Security headers outermost, so they are on every response: a CSRF refusal
	// and a 404 included.
	return middleware.SecurityHeaders(csrf)
}

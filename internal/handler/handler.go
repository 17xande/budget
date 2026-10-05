// Package handler is the HTTP layer: routes, pages and rendering. It depends on the
// domain packages; they never depend on it.
package handler

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/auth"
	"github.com/17xande-dev/budget/internal/ledger"
	"github.com/17xande-dev/budget/internal/middleware"
	"github.com/justinas/nosurf"
	"golang.org/x/time/rate"
)

type Config struct {
	CookieSecure bool
	SessionTTL   time.Duration
}

type Handler struct {
	users        *auth.Store
	ledger       *ledger.Store
	cookieSecure bool
	sessionTTL   time.Duration

	tmpls  map[string]*template.Template
	assets map[string]staticAsset

	// loginByEmail charges failed sign-ins against the address they named. It
	// is the tight tier, keyed on something an attacker cannot discard the way
	// they can a cookie; the per-IP limiter wrapping the route is the looser
	// ceiling behind it. Only failures are charged, so signing in successfully a
	// few times in a row never locks anyone out.
	loginByEmail *middleware.Limiter

	// routes is every protected route as registered; see Register.
	routes []Route
}

func New(users *auth.Store, led *ledger.Store, cfg Config) (*Handler, error) {
	h := &Handler{
		users:        users,
		ledger:       led,
		cookieSecure: cfg.CookieSecure,
		sessionTTL:   cfg.SessionTTL,
		loginByEmail: middleware.NewLimiter(rate.Every(time.Minute), 5),
	}
	var err error
	if h.assets, err = loadAssets(); err != nil {
		return nil, err
	}
	if h.tmpls, err = h.parseTemplates(); err != nil {
		return nil, err
	}
	return h, nil
}

// page is embedded in every page's data, so something every page needs is one
// change here rather than one per page.
type page struct {
	Title     string
	CSRFToken string
	// Notice is a short confirmation after a redirect, resolved from a fixed
	// code map -- never echoed from the query string. See notice().
	Notice string
	// Me is the signed-in user; zero on login and setup.
	Me auth.User
	// Nav names the section the page belongs to, for marking the current link.
	Nav string
}

// notices maps the ?notice= codes redirects carry to the text shown. A fixed map,
// because rendering the parameter itself is a reflected-content hole.
var notices = map[string]string{
	"saved":                "Saved.",
	"imported":             "Import finished. Each file's result is in the history below.",
	"member_added":         "Member added. Give them the temporary password; they will choose their own when they first sign in.",
	"password_changed":     "Password changed. Please sign in again.",
	"must_change_password": "This password was set for you. Choose your own before continuing.",
}

func notice(r *http.Request) string {
	return notices[r.URL.Query().Get("notice")]
}

func newPage(r *http.Request, title, nav string) page {
	p := page{Title: title, CSRFToken: nosurf.Token(r), Notice: notice(r), Nav: nav}
	p.Me, _ = middleware.User(r)
	return p
}

// Route is one protected route as it was registered. The method is part of it
// because a POST-registered route answers a GET with 405 before any middleware
// runs, so a GET-only sweep would report it protected without ever exercising
// the protection.
type Route struct {
	Method  string
	Pattern string
}

// ownPasswordPath is the one protected page someone with a must-change password
// can reach.
const ownPasswordPath = "/password"

// Register mounts every route.
//
// Protected routes go through the route() closure rather than a prefix wrapped
// somewhere else, so the protection travels with the route: a middleware applied
// to a prefix is one refactor away from silently not covering a route added here.
// The few public routes are mounted directly, and a test pins exactly which.
//
// protect is RequireUser. limit is the per-IP limiter for every route that
// verifies a secret: the login, the setup claim and the own-password change.
func (h *Handler) Register(mux *http.ServeMux, protect, limit func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET "+middleware.StaticPrefix+"{file...}", h.ServeStatic)
	mux.HandleFunc("GET /login", h.LoginForm)
	mux.Handle("POST /login", limit(http.HandlerFunc(h.Login)))
	// Setup has no account to sign in as yet; both routes 404 once one exists.
	mux.HandleFunc("GET /setup", h.SetupForm)
	mux.Handle("POST /setup", limit(http.HandlerFunc(h.SetupClaim)))
	// Outside protect so signing out works with a session that has already gone.
	mux.HandleFunc("POST /logout", h.Logout)

	h.routes = nil
	route := func(pattern string, handler http.HandlerFunc, extra ...func(http.Handler) http.Handler) {
		method, p, ok := strings.Cut(pattern, " ")
		if !ok {
			panic("route pattern must be \"METHOD /path\": " + pattern)
		}
		h.routes = append(h.routes, Route{Method: method, Pattern: p})
		var next http.Handler = handler
		if p != ownPasswordPath {
			next = h.requireOwnPassword(next)
		}
		for _, mw := range extra {
			next = mw(next)
		}
		// protect outermost, so an anonymous request is redirected without
		// spending anyone's rate-limit budget.
		mux.Handle(pattern, protect(next))
	}

	route("GET /{$}", h.Home)
	route("GET /transactions", h.Transactions)
	route("GET /import", h.ImportPage)
	route("POST /import", h.ImportUpload)
	route("GET /accounts", h.Accounts)
	route("POST /accounts/{id}", h.AccountUpdate)
	route("GET /members", h.Members)
	route("POST /members", h.MemberCreate)
	route("GET "+ownPasswordPath, h.OwnPasswordForm)
	route("POST "+ownPasswordPath, h.OwnPasswordChange, limit)
}

// Routes is every protected route Register mounted, derived rather than
// hand-listed so a test asserting each refuses an anonymous request cannot fall
// out of step with what is mounted.
func (h *Handler) Routes() []Route { return h.routes }

// TestPath renders a pattern as a requestable path: {$} dropped, wildcards filled.
func (r Route) TestPath() string {
	segs := strings.Split(r.Pattern, "/")
	for i, s := range segs {
		switch {
		case s == "{$}":
			segs[i] = ""
		case strings.HasPrefix(s, "{"):
			segs[i] = "x"
		}
	}
	return strings.Join(segs, "/")
}

// requireOwnPassword bounces someone whose password was chosen by somebody else
// to the change form before anything else opens.
func (h *Handler) requireOwnPassword(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := middleware.User(r); ok && u.MustChangePassword {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", ownPasswordPath+"?notice=must_change_password")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			http.Redirect(w, r, ownPasswordPath+"?notice=must_change_password", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionCookie builds the session cookie, for setting and clearing it; a
// negative ttl clears it.
//
// SameSite is always Lax: a credential must never ride along on a cross-site
// request, leaving the CSRF token as the only thing in the way.
func (h *Handler) sessionCookie(value string, ttl time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
	if ttl > 0 {
		c.Expires = time.Now().Add(ttl)
		c.MaxAge = int(ttl.Seconds())
	} else {
		c.MaxAge = -1
	}
	return c
}

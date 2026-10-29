// Package middleware holds the HTTP layers that wrap every route: security
// headers, session enforcement and rate limiting.
package middleware

import (
	"net/http"
	"strings"
)

// CSP is the one content security policy, for every response.
//
// Everything is served from this origin: the stylesheet, the vendored htmx and the
// Deno-built bundle are all embedded in the binary. So the policy allows 'self' and
// nothing else, with one deliberate exception: the typefaces come from Google Fonts,
// so style-src also names https://fonts.googleapis.com (the font stylesheet) and
// font-src names https://fonts.gstatic.com (the files). No script and no connection
// goes to either, and there is no 'unsafe-inline' for either styles or scripts.
// That is what makes the zero-inline-style and zero-on*= rules in the templates
// load-bearing rather than tidiness: an onchange= renders, returns 200, passes
// every handler test, and silently does nothing in a real browser.
//
// connect-src 'self' because htmx swaps are XHR; script-src covers only the
// <script> tag, so without it every hx-get is refused with nothing but a console
// message to say why.
const CSP = "default-src 'none'; style-src 'self' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; " +
	"script-src 'self'; connect-src 'self'; " +
	"img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// StaticPrefix is the asset tree. Its responses are content-addressed and
// cacheable; everything else holds someone's finances and is not.
const StaticPrefix = "/static/"

// SecurityHeaders sets the security headers on every response, including a 404,
// which is why it wraps the mux rather than living in a handler.
//
// No HSTS: a browser ignores it over plain http, and sent from localhost it pins a
// rule that breaks the next project on that port. Traefik terminates TLS and is
// the right place for it.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		// A private tool; no reason for a crawler to hold a map of it.
		h.Set("X-Robots-Tag", "noindex, nofollow")
		if !strings.HasPrefix(r.URL.Path, StaticPrefix) {
			// Bank transactions should not sit in a shared cache, or in the
			// browser's back-forward cache after signing out.
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

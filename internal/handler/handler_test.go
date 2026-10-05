package handler_test

import (
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/budget/internal/auth"
	"github.com/17xande-dev/budget/internal/budget"
	"github.com/17xande-dev/budget/internal/db"
	"github.com/17xande-dev/budget/internal/handler"
	"github.com/17xande-dev/budget/internal/ledger"
	"github.com/17xande-dev/budget/internal/middleware"
	"golang.org/x/time/rate"
)

const setupToken = "test-setup-token-that-is-long-enough"

type env struct {
	t     *testing.T
	srv   *httptest.Server
	users *auth.Store
	h     *handler.Handler
}

// newEnv runs the production stack (handler.Server) over a fresh database with a
// setup token issued. The secret limiter is effectively unlimited so tests that
// sign in repeatedly measure behaviour, not the ceiling; the per-email tier inside
// Login is the real one and stays in force.
func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, handler.Config{SessionTTL: time.Hour})
}

// newEnvWith is newEnv with a chosen handler configuration.
func newEnvWith(t *testing.T, cfg handler.Config) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	users := auth.NewStore(d)
	if _, err := users.CreateSetupToken(setupToken, time.Now()); err != nil {
		t.Fatal(err)
	}
	h, err := handler.New(users, ledger.NewStore(d), budget.NewStore(d), cfg)
	if err != nil {
		t.Fatal(err)
	}
	limit := middleware.NewLimiter(rate.Inf, 1).ByIP(nil)
	srv := httptest.NewServer(h.Server(middleware.RequireUser(users), limit, nil))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, users: users, h: h}
}

// browser is a client with its own cookie jar that does not follow redirects, so
// tests assert on where a response sends you.
type browser struct {
	e *env
	c *http.Client
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type resp struct {
	code int
	loc  string
	body string
	hdr  http.Header
}

func (b *browser) do(req *http.Request) resp {
	b.e.t.Helper()
	res, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, loc: res.Header.Get("Location"), body: string(body), hdr: res.Header}
}

func (b *browser) get(path string) resp {
	b.e.t.Helper()
	req, _ := http.NewRequest("GET", b.e.srv.URL+path, nil)
	return b.do(req)
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// token fetches a page and returns its CSRF token, as a browser would have one
// from the form it is about to submit.
func (b *browser) token(path string) string {
	b.e.t.Helper()
	r := b.get(path)
	m := csrfRe.FindStringSubmatch(r.body)
	if m == nil {
		b.e.t.Fatalf("no CSRF token on %s (status %d)", path, r.code)
	}
	// Unescaped as a browser would: the token is base64, and html/template
	// writes a "+" in an attribute as "&#43;".
	return html.UnescapeString(m[1])
}

// post submits a form from page `from`, carrying its CSRF token and the Origin a
// browser sends.
func (b *browser) post(from, path string, form url.Values) resp {
	b.e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", b.token(from))
	return b.postRaw(path, form)
}

func (b *browser) postRaw(path string, form url.Values) resp {
	b.e.t.Helper()
	req, _ := http.NewRequest("POST", b.e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.e.srv.URL)
	return b.do(req)
}

const ownerPassword = "owner passphrase one"

// claim completes setup as the owner and returns their signed-in browser.
func (e *env) claim() *browser {
	e.t.Helper()
	b := e.browser()
	r := b.post("/setup", "/setup", url.Values{
		"token": {" " + setupToken + "\n"}, "email": {"Owner <owner@example.com>"}, "name": {"Owner"},
		"password": {ownerPassword}, "password_confirm": {ownerPassword},
	})
	if r.code != http.StatusSeeOther || r.loc != "/" {
		e.t.Fatalf("setup claim: %d -> %q\n%s", r.code, r.loc, r.body)
	}
	return b
}

func (b *browser) login(email, password string) resp {
	b.e.t.Helper()
	return b.post("/login", "/login", url.Values{"email": {email}, "password": {password}})
}

func TestSetupFlow(t *testing.T) {
	e := newEnv(t)
	anon := e.browser()
	if r := anon.get("/login"); r.code != http.StatusSeeOther || r.loc != "/setup" {
		t.Fatalf("unclaimed /login should send you to /setup: %d %q", r.code, r.loc)
	}
	// A wrong token is refused and never echoed back into the form.
	r := anon.post("/setup", "/setup", url.Values{
		"token": {"wrong-token"}, "email": {"a@example.com"}, "name": {"A"},
		"password": {ownerPassword}, "password_confirm": {ownerPassword},
	})
	if r.code != http.StatusUnauthorized || strings.Contains(r.body, "wrong-token") {
		t.Fatalf("wrong token: %d, echoed=%v", r.code, strings.Contains(r.body, "wrong-token"))
	}

	owner := e.claim()
	if r := owner.get("/"); r.code != 200 || !strings.Contains(r.body, "Hello, Owner") {
		t.Fatalf("home after claim: %d", r.code)
	}
	// The display-name form was normalised, so the bare address signs in.
	if _, ok, _ := e.users.FindByEmail("owner@example.com"); !ok {
		t.Error("claimed account was not stored under its bare address")
	}
	// Setup does not exist on a claimed deployment.
	if r := anon.get("/setup"); r.code != http.StatusNotFound {
		t.Errorf("/setup after claim: %d, want 404", r.code)
	}
}

func TestMemberJourney(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()

	const temp = "temporary passphrase"
	r := owner.post("/members", "/members", url.Values{
		"email": {"partner@example.com"}, "name": {"Partner"},
		"password": {temp}, "password_confirm": {temp},
	})
	if r.code != http.StatusSeeOther || r.loc != "/members?notice=member_added" {
		t.Fatalf("add member: %d %q\n%s", r.code, r.loc, r.body)
	}
	if r := owner.get("/members"); !strings.Contains(r.body, "partner@example.com") || !strings.Contains(r.body, "temporary password") {
		t.Error("members page does not list the new member as on a temporary password")
	}
	// A duplicate, differently cased, is a field error rather than a 500.
	r = owner.post("/members", "/members", url.Values{
		"email": {"PARTNER@example.com"}, "name": {"Again"}, "password": {temp}, "password_confirm": {temp},
	})
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "already has an account") {
		t.Errorf("duplicate member: %d", r.code)
	}

	partner := e.browser()
	if r := partner.login("partner@example.com", temp); r.code != http.StatusSeeOther {
		t.Fatalf("partner login: %d", r.code)
	}
	// Somebody else chose this password: nothing opens until they choose their own.
	if r := partner.get("/"); r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/password") {
		t.Fatalf("must-change partner reached home: %d %q", r.code, r.loc)
	}
	// Re-using the temporary password as "their own" is refused.
	r = partner.post("/password", "/password", url.Values{"current": {temp}, "password": {temp}, "password_confirm": {temp}})
	if r.code != http.StatusUnprocessableEntity {
		t.Errorf("reusing the temporary password: %d", r.code)
	}
	const mine = "partners own passphrase"
	r = partner.post("/password", "/password", url.Values{"current": {temp}, "password": {mine}, "password_confirm": {mine}})
	if r.code != http.StatusSeeOther || r.loc != "/login?notice=password_changed" {
		t.Fatalf("change password: %d %q", r.code, r.loc)
	}
	// The change ended the session it was made from.
	if r := partner.get("/"); r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/login") {
		t.Errorf("session survived a password change: %d %q", r.code, r.loc)
	}
	partner.login("partner@example.com", mine)
	if r := partner.get("/"); r.code != 200 {
		t.Errorf("partner home after choosing own password: %d", r.code)
	}
}

func TestLoginFailures(t *testing.T) {
	e := newEnv(t)
	e.claim()
	b := e.browser()

	r := b.login("owner@example.com", "wrong password!!")
	if r.code != http.StatusUnauthorized || !strings.Contains(r.body, "Email or password is incorrect.") {
		t.Fatalf("wrong password: %d", r.code)
	}
	unknown := b.login("nobody@example.com", "wrong password!!")
	// One message for every cause, or the form tells you who has an account.
	if unknown.code != r.code || !strings.Contains(unknown.body, "Email or password is incorrect.") {
		t.Error("an unknown address is answered differently from a wrong password")
	}

	// Five failures exhaust the address; the right password is then refused too,
	// until the bucket refills. Another address is unaffected.
	for range 4 {
		b.login("owner@example.com", "wrong password!!")
	}
	if r := b.login("owner@example.com", ownerPassword); r.code != http.StatusTooManyRequests {
		t.Errorf("sixth attempt after five failures: %d, want 429", r.code)
	}
	if r := b.login("someone@example.com", "whatever whatever"); r.code != http.StatusUnauthorized {
		t.Errorf("a different address was limited: %d", r.code)
	}
}

// Successful sign-ins are not charged, so using the app normally never locks
// anyone out.
func TestSuccessfulLoginsAreNotCharged(t *testing.T) {
	e := newEnv(t)
	e.claim()
	for i := range 8 {
		if r := e.browser().login("owner@example.com", ownerPassword); r.code != http.StatusSeeOther {
			t.Fatalf("sign-in %d: %d", i+1, r.code)
		}
	}
}

func TestOpenRedirectRefused(t *testing.T) {
	e := newEnv(t)
	e.claim()
	for next, want := range map[string]string{
		"/members":             "/members",
		"https://evil.example": "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
		"":                     "/",
	} {
		b := e.browser()
		r := b.post("/login", "/login", url.Values{"email": {"owner@example.com"}, "password": {ownerPassword}, "next": {next}})
		if r.loc != want {
			t.Errorf("next=%q redirected to %q, want %q", next, r.loc, want)
		}
	}
}

func TestCSRFRequired(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	owner.get("/members") // a page visit, so the CSRF cookie is set
	r := owner.postRaw("/members", url.Values{"email": {"x@example.com"}, "name": {"X"}})
	if r.code != http.StatusForbidden {
		t.Fatalf("POST without a CSRF token: %d, want 403", r.code)
	}
	if r.hdr.Get("Content-Security-Policy") == "" {
		t.Error("a CSRF refusal went out without security headers")
	}
}

// Every protected route must refuse an anonymous request -- including the POSTs,
// which is why the request carries a valid CSRF token: without one, nosurf would
// refuse it first and the protection itself would never be exercised.
func TestEveryProtectedRouteRefusesAnonymous(t *testing.T) {
	e := newEnv(t)
	e.claim()
	anon := e.browser()
	routes := e.h.Routes()
	if len(routes) < 5 {
		t.Fatalf("only %d protected routes recorded; is Register still recording them?", len(routes))
	}
	for _, rt := range routes {
		var r resp
		if rt.Method == "POST" {
			r = anon.post("/login", rt.TestPath(), nil)
		} else {
			r = anon.get(rt.TestPath())
		}
		if r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/login?next=") {
			t.Errorf("%s %s answered an anonymous request with %d %q", rt.Method, rt.Pattern, r.code, r.loc)
		}
	}

	// htmx gets a 401 and a reload instruction, not a login page swapped into a fragment.
	req, _ := http.NewRequest("GET", e.srv.URL+"/members", nil)
	req.Header.Set("HX-Request", "true")
	if r := anon.do(req); r.code != http.StatusUnauthorized || r.hdr.Get("HX-Refresh") != "true" {
		t.Errorf("anonymous htmx request: %d HX-Refresh=%q", r.code, r.hdr.Get("HX-Refresh"))
	}
}

// Routes mounted outside the route() closure are unprotected and invisible to the
// sweep above, so the set of direct mux.Handle calls is pinned by source text. A
// new public route is a deliberate edit here.
func TestPublicRoutesPinned(t *testing.T) {
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\(([^,]+),`)
	var got []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		got = append(got, m[1])
	}
	want := []string{
		`"GET /health"`,
		`"GET "+middleware.StaticPrefix+"{file...}"`,
		`"GET /login"`,
		`"POST /login"`,
		`"GET /setup"`,
		`"POST /setup"`,
		`"POST /logout"`,
		`pattern`, // inside route(), behind protect
	}
	if !slices.Equal(got, want) {
		t.Errorf("direct mux registrations changed:\n got %q\nwant %q", got, want)
	}
}

var inlineRe = regexp.MustCompile(`(?i)\sstyle\s*=|\son[a-z]+\s*=`)

// The CSP allows no inline style or script. An onchange= or style= renders, returns
// 200 and passes every handler test -- and does nothing (or is unstyled) in a real
// browser. Checked in the source and in every page the journey renders.
func TestNoInlineStylesOrHandlers(t *testing.T) {
	n := 0
	err := fs.WalkDir(os.DirFS("templates"), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(filepath.Join("templates", p))
		if err != nil {
			return err
		}
		n++
		if loc := inlineRe.FindIndex(b); loc != nil {
			t.Errorf("templates/%s has an inline style or handler: %q", p, b[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 8 {
		t.Fatalf("checked only %d template files", n)
	}

	e := newEnv(t)
	anon := e.browser()
	pages := map[string]string{"/setup": anon.get("/setup").body}
	owner := e.claim()
	for _, p := range []string{"/", "/members", "/password"} {
		pages[p] = owner.get(p).body
	}
	pages["/login"] = e.browser().get("/login").body
	for p, body := range pages {
		if !strings.Contains(body, "</html>") {
			t.Errorf("%s did not render a page", p)
		}
		if loc := inlineRe.FindStringIndex(body); loc != nil {
			t.Errorf("%s renders an inline style or handler: %q", p, body[loc[0]:loc[1]])
		}
	}
}

var assetRe = regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`)

func TestStaticAssets(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	page := b.get("/setup").body
	urls := assetRe.FindAllStringSubmatch(page, -1)
	if len(urls) != 3 {
		t.Fatalf("expected stylesheet, htmx and app.js on the page, found %d", len(urls))
	}
	for _, m := range urls {
		r := b.get(m[1])
		if r.code != 200 || !strings.Contains(r.hdr.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: %d cache=%q", m[1], r.code, r.hdr.Get("Cache-Control"))
		}
		if strings.Contains(m[1], "/missing/") {
			t.Errorf("page references a missing asset: %s", m[1])
		}
	}
	// Embedded but deliberately unserved.
	if r := b.get("/static/README.md"); r.code != http.StatusNotFound {
		t.Errorf("README.md served: %d", r.code)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	r := owner.post("/", "/logout", nil)
	if r.code != http.StatusSeeOther || r.loc != "/login" {
		t.Fatalf("logout: %d %q", r.code, r.loc)
	}
	if r := owner.get("/"); r.code != http.StatusSeeOther {
		t.Errorf("still signed in after logout: %d", r.code)
	}
}

package handler

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/17xande/budget/internal/auth"
	"github.com/17xande/budget/internal/middleware"
	"github.com/17xande/budget/internal/validate"
)

// homePath is where a sign-in lands when there is no ?next=.
const homePath = "/"

type loginData struct {
	page
	// Error is the one message every failure produces. Per-field messages would
	// say which field was wrong, which is a way to find out who has an account.
	Error string
	Email string
	Next  string
}

// LoginForm renders the sign-in page. Before the deployment is claimed it sends
// you to /setup instead, and someone already signed in goes straight on.
func (h *Handler) LoginForm(w http.ResponseWriter, r *http.Request) {
	pending, ok := h.setupPending(w)
	if !ok {
		return
	}
	if pending {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if h.signedIn(r) {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	h.render(w, http.StatusOK, "login.html", loginData{
		page: newPage(r, "Sign in", ""),
		Next: safeNext(r.URL.Query().Get("next")),
	})
}

// Login verifies credentials and issues a session. A failure re-renders the form
// at 401 rather than redirecting: that is how the message survives without a flash
// mechanism, and there is nothing to re-submit on a page that signed nobody in.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	email := validate.NormalizeEmail(r.PostFormValue("email"))
	next := safeNext(r.PostFormValue("next"))
	fail := func(status int, msg string) {
		h.render(w, status, "login.html", loginData{
			page:  newPage(r, "Sign in", ""),
			Error: msg,
			Email: email,
			Next:  next,
		})
	}

	// Checked before verifying, so a locked address costs no argon2 at all.
	emailKey := "email:" + strings.ToLower(email)
	if h.loginByEmail.Exhausted(emailKey) {
		fail(http.StatusTooManyRequests, "Too many failed attempts for this address. Wait a minute and try again.")
		return
	}

	user, ok, err := h.users.Authenticate(email, r.PostFormValue("password"), time.Now())
	if err != nil {
		log.Printf("login: authenticate: %v", err)
		fail(http.StatusInternalServerError, "Something went wrong signing you in. Please try again.")
		return
	}
	if !ok {
		h.loginByEmail.Allow(emailKey)
		// Logged without the password, with the address, so a typo can be told
		// from someone working through a list.
		log.Printf("login failed for %q", email)
		fail(http.StatusUnauthorized, "Email or password is incorrect.")
		return
	}

	token, err := h.users.IssueSession(user.ID, time.Now(), h.sessionTTL)
	if err != nil {
		log.Printf("login: issue session for %d: %v", user.ID, err)
		fail(http.StatusInternalServerError, "Something went wrong signing you in. Please try again.")
		return
	}
	http.SetCookie(w, h.sessionCookie(token, h.sessionTTL))
	log.Printf("signed in: %s", user.Email)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// Logout deletes the session and clears the cookie. POST only: a GET would let
// any page sign you out with an <img>.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(middleware.SessionCookieName); err == nil {
		if err := h.users.DeleteSession(c.Value); err != nil {
			// The cookie is cleared regardless, so this browser is signed out
			// either way; the row outliving it is worth knowing about.
			log.Printf("logout: delete session: %v", err)
		}
	}
	http.SetCookie(w, h.sessionCookie("", -time.Hour))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) signedIn(r *http.Request) bool {
	c, err := r.Cookie(middleware.SessionCookieName)
	if err != nil {
		return false
	}
	_, ok, err := h.users.VerifySession(c.Value, time.Now())
	return err == nil && ok
}

// safeNext sanitises a ?next= target to a path on this server. An absolute URL
// would make the login form an open redirect -- a ready-made phishing step.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.HasPrefix(next, "/\\") || strings.ContainsAny(next, "\r\n") {
		return homePath
	}
	return next
}

// The first-run claim: /setup exchanges the one-time token printed on a boot with
// no accounts for the first account. Both routes 404 once claimed -- a set-up
// deployment should not advertise the shape of its bootstrap.

type setupData struct {
	page
	// The token is deliberately never carried back into a rejected form: a
	// credential that survives a failed submission ends up in form history and
	// screenshots. Email and name are kept; retyping them is pure annoyance.
	Email  string
	Name   string
	Errors validate.FormErrors
}

func (h *Handler) SetupForm(w http.ResponseWriter, r *http.Request) {
	pending, ok := h.setupPending(w)
	if !ok {
		return
	}
	if !pending {
		http.NotFound(w, r)
		return
	}
	h.render(w, http.StatusOK, "setup.html", setupData{page: newPage(r, "Set up", "")})
}

func (h *Handler) SetupClaim(w http.ResponseWriter, r *http.Request) {
	pending, ok := h.setupPending(w)
	if !ok {
		return
	}
	if !pending {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	email := validate.NormalizeEmail(r.PostFormValue("email"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	// Trimmed: it is copied out of `docker compose logs`, where a line selection
	// picks up a trailing newline and a correct token is then refused with
	// nothing on the page to say whitespace was the problem.
	token := strings.TrimSpace(r.PostFormValue("token"))
	password := r.PostFormValue("password")

	again := func(status int, errs validate.FormErrors) {
		h.render(w, status, "setup.html", setupData{
			page: newPage(r, "Set up", ""), Email: email, Name: name, Errors: errs,
		})
	}
	errs := validate.ValidateMember(email, name)
	validate.ValidatePassword(errs, password, r.PostFormValue("password_confirm"))
	if token == "" {
		errs["token"] = "Enter the setup token from the server log."
	}
	if len(errs) > 0 {
		again(http.StatusUnprocessableEntity, errs)
		return
	}

	hash, err := auth.HashPassword(password, auth.DefaultParams)
	if err != nil {
		log.Printf("setup: hash password: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	user, err := h.users.ClaimSetup(token, email, name, hash, time.Now())
	switch {
	case errors.Is(err, auth.ErrBadSetupToken):
		log.Printf("setup: wrong setup token offered")
		again(http.StatusUnauthorized, validate.FormErrors{"token": "That setup token is not right."})
		return
	case errors.Is(err, auth.ErrSetupClosed):
		http.NotFound(w, r)
		return
	case err != nil:
		log.Printf("setup: claim: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Signed straight in: they hold the setup token and just chose this
	// password, which is more than the login form asks for.
	session, err := h.users.IssueSession(user.ID, time.Now(), h.sessionTTL)
	if err != nil {
		log.Printf("setup: issue session for %d: %v", user.ID, err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, h.sessionCookie(session, h.sessionTTL))
	log.Printf("setup: %s claimed the first account", user.Email)
	http.Redirect(w, r, homePath, http.StatusSeeOther)
}

// setupPending reports whether the deployment is still claimable. ok is false when
// it could not find out, in which case a 500 has been written and the caller must
// return: "not pending" and "cannot tell" need different answers, and guessing
// either way is wrong (a 404 during a database blip, or a claim form on a
// deployment that has accounts).
func (h *Handler) setupPending(w http.ResponseWriter) (pending, ok bool) {
	pending, err := h.users.SetupPending()
	if err != nil {
		log.Printf("setup pending: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return false, false
	}
	return pending, true
}

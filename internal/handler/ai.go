package handler

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/auth"
	"github.com/17xande-dev/budget/internal/budget"
	"github.com/17xande-dev/budget/internal/middleware"
)

// --- AI access: API tokens for MCP clients -------------------------------------

// tokenLifetimes are the choices offered, by form value. A fixed set rather than
// a number field, parsed strictly: an absent or unknown value is a bad request.
var tokenLifetimes = map[string]time.Duration{
	"30":  30 * 24 * time.Hour,
	"90":  90 * 24 * time.Hour,
	"365": 365 * 24 * time.Hour,
}

type tokensData struct {
	page
	Tokens []auth.APIToken
	// NewToken is shown exactly once, on the response to creating it, and is
	// never stored or rendered again.
	NewToken string
	NewName  string
	Endpoint string
	Error    string
	Now      time.Time
}

func (h *Handler) Tokens(w http.ResponseWriter, r *http.Request) {
	h.renderTokens(w, r, http.StatusOK, tokensData{})
}

func (h *Handler) renderTokens(w http.ResponseWriter, r *http.Request, status int, d tokensData) {
	me, _ := middleware.User(r)
	toks, err := h.users.APITokens(me.ID)
	if err != nil {
		h.fail(w, "tokens: list", err)
		return
	}
	d.page = newPage(r, "AI access", "settings")
	d.Tokens, d.Endpoint, d.Now = toks, h.mcpEndpoint(r), time.Now()
	h.render(w, status, "tokens.html", d)
}

// mcpEndpoint is the URL an MCP client connects to: this server's own origin, as
// the browser reached it, plus /mcp.
func (h *Handler) mcpEndpoint(r *http.Request) string {
	scheme := "http"
	if h.cookieSecure {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: "/mcp"}).String()
}

// TokenCreate issues a token and shows it once. This is the one form answered
// with a page rather than a redirect: the token cannot be put in a URL to show
// after one, and must not be stored anywhere it could be shown again.
func (h *Handler) TokenCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	ttl, ok := tokenLifetimes[r.PostFormValue("days")]
	if !ok {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	me, _ := middleware.User(r)
	name := strings.TrimSpace(r.PostFormValue("name"))
	token, _, err := h.users.IssueAPIToken(me.ID, name, ttl, time.Now())
	if errors.Is(err, auth.ErrInvalidTokenName) {
		h.renderTokens(w, r, http.StatusUnprocessableEntity, tokensData{Error: "Give the token a name of up to 60 characters, like the device or app it is for."})
		return
	} else if err != nil {
		h.fail(w, "tokens: issue", err)
		return
	}
	log.Printf("api token %q created by %s", name, me.Email)
	h.renderTokens(w, r, http.StatusOK, tokensData{NewToken: token, NewName: name})
}

func (h *Handler) TokenRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	me, _ := middleware.User(r)
	if err := h.users.RevokeAPIToken(me.ID, id); err != nil {
		if errors.Is(err, auth.ErrTokenNotFound) {
			http.NotFound(w, r)
			return
		}
		h.fail(w, "tokens: revoke", err)
		return
	}
	log.Printf("api token %d revoked by %s", id, me.Email)
	http.Redirect(w, r, "/tokens?notice=token_revoked", http.StatusSeeOther)
}

// --- Activity: every change, and undo ------------------------------------------

type activityData struct {
	page
	Entries []budget.Entry
	// AIOnly filters to changes made through MCP, the view that matters for
	// "what did the assistant do".
	AIOnly bool
	Error  string
}

func (h *Handler) Activity(w http.ResponseWriter, r *http.Request) {
	h.renderActivity(w, r, http.StatusOK, "")
}

func (h *Handler) renderActivity(w http.ResponseWriter, r *http.Request, status int, msg string) {
	aiOnly := r.URL.Query().Get("via") != "all"
	via := budget.ViaMCP
	if !aiOnly {
		via = ""
	}
	entries, err := h.budget.Activity(via, 200)
	if err != nil {
		h.fail(w, "activity", err)
		return
	}
	h.render(w, status, "activity.html", activityData{page: newPage(r, "Activity", "settings"), Entries: entries, AIOnly: aiOnly, Error: msg})
}

// Undo reverses one categorisation. It refuses, rather than clobbering, when the
// transaction has been changed again since.
func (h *Handler) Undo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	err = h.budget.Undo(id, webActor(r))
	ret := safeReturn(r.PostFormValue("return"), "/activity")
	switch {
	case err == nil:
		sep := "?"
		if strings.Contains(ret, "?") {
			sep = "&"
		}
		http.Redirect(w, r, ret+sep+"notice=undone", http.StatusSeeOther)
	case errors.Is(err, budget.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, budget.ErrChangedSince), errors.Is(err, budget.ErrAlreadyUndone), errors.Is(err, budget.ErrInvalid):
		h.renderActivity(w, r, http.StatusConflict, err.Error())
	default:
		h.fail(w, "undo", err)
	}
}

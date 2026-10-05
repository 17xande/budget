package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/17xande/budget/internal/fetch"
	"github.com/17xande/budget/internal/middleware"
)

type fetchData struct {
	page
	Configured bool
	Runs       []fetch.Run
	Next       time.Time
	Error      string
}

// FetchStatus is the bank fetch's status page: whether it is configured, when it
// next runs, and every recent attempt with what it imported or why it failed.
func (h *Handler) FetchStatus(w http.ResponseWriter, r *http.Request) {
	h.renderFetch(w, r, http.StatusOK, "")
}

func (h *Handler) renderFetch(w http.ResponseWriter, r *http.Request, status int, msg string) {
	d := fetchData{page: newPage(r, "Bank fetch", "settings"), Error: msg}
	if h.fetcher != nil {
		runs, err := h.fetcher.Runs(30)
		if err != nil {
			h.fail(w, "fetch: runs", err)
			return
		}
		d.Runs, d.Configured = runs, h.fetcher.Configured()
		d.Next = h.fetcher.NextRun(time.Now())
	}
	h.render(w, status, "fetch.html", d)
}

// FetchNow starts a fetch in the background. The guards answer here, so a
// refusal (already running, ran minutes ago) is shown rather than logged.
func (h *Handler) FetchNow(w http.ResponseWriter, r *http.Request) {
	if h.fetcher == nil {
		http.NotFound(w, r)
		return
	}
	me, _ := middleware.User(r)
	err := h.fetcher.RunNow(me.ID)
	switch {
	case err == nil:
		http.Redirect(w, r, "/fetch?notice=fetch_started", http.StatusSeeOther)
	case errors.Is(err, fetch.ErrBusy), errors.Is(err, fetch.ErrTooSoon), errors.Is(err, fetch.ErrNotConfigured):
		h.renderFetch(w, r, http.StatusConflict, err.Error())
	default:
		h.fail(w, "fetch: run now", err)
	}
}

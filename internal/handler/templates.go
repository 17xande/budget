package handler

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/17xande/budget/internal/budget"
	"github.com/17xande/budget/internal/money"
)

//go:embed templates
var templateFS embed.FS

// Every page under templates/pages defines "content" and renders through a layout
// that defines "layout". They are parsed one set per page: in a single flat set the
// last file defining "content" silently wins and pages render as each other.
const (
	partialsGlob = "templates/partials/*.html"
	layoutsDir   = "templates/layouts"
	pagesDir     = "templates/pages"
)

// pageLayouts maps a page to the layout it renders through; the default is the
// signed-in chrome. Login and setup render for someone with no session, so they
// use a layout with no nav and no sign-out button in scope at all -- structural,
// rather than a flag every page has to remember.
var pageLayouts = map[string]string{
	"login.html": "plain.html",
	"setup.html": "plain.html",
}

const defaultLayout = "app.html"

// parseTemplates builds one template set per page. Every failure is a startup
// failure, including a page that defines no "content" and would render as empty
// chrome.
func (h *Handler) parseTemplates() (map[string]*template.Template, error) {
	base, err := template.New("app").Funcs(h.templateFuncs()).ParseFS(templateFS, partialsGlob)
	if err != nil {
		return nil, fmt.Errorf("parse partials: %w", err)
	}
	pages, err := fs.ReadDir(templateFS, pagesDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pagesDir, err)
	}
	out := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		if p.IsDir() || path.Ext(p.Name()) != ".html" {
			continue
		}
		layout := defaultLayout
		if l, ok := pageLayouts[p.Name()]; ok {
			layout = l
		}
		set, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone base for %s: %w", p.Name(), err)
		}
		if set, err = set.ParseFS(templateFS, layoutsDir+"/"+layout); err != nil {
			return nil, fmt.Errorf("parse layout %s for %s: %w", layout, p.Name(), err)
		}
		if set, err = set.ParseFS(templateFS, pagesDir+"/"+p.Name()); err != nil {
			return nil, fmt.Errorf("parse page %s: %w", p.Name(), err)
		}
		if set.Lookup("content") == nil {
			return nil, fmt.Errorf("page %s defines no \"content\" template", p.Name())
		}
		out[p.Name()] = set
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no pages found in %s", pagesDir)
	}
	return out, nil
}

// templateFuncs is deliberately tiny.
func (h *Handler) templateFuncs() template.FuncMap {
	return template.FuncMap{
		"asset":     h.assetURL,
		"money":     formatMoney,
		"rand":      formatRand,
		"lineState": lineState,
		"abs": func(n int64) int64 {
			if n < 0 {
				return -n
			}
			return n
		},
		"moneyClass": moneyClass,
		"month":      monthLabel,
		"date":       dateLabel,
		"decimal":    money.Decimal,
		// idle names the expense categories with nothing spent and no target,
		// so the dashboard can list them in one line instead of a row of
		// zeros each.
		"idle": func(lines []budget.Line) []string {
			var out []string
			for _, l := range lines {
				if l.Count == 0 && !l.Target.Valid {
					out = append(out, l.Name)
				}
			}
			return out
		},
		"join":       strings.Join,
		"action":     actionLabel,
		"shiftMonth": shiftMonth,
		// barWidth is a progress bar's filled share, 0-100, as an SVG width
		// attribute -- an attribute, not a style, so the CSP stays strict.
		"barWidth": func(spent, target int64) int64 {
			switch {
			case target <= 0 || spent <= 0:
				return 0
			case spent >= target:
				return 100
			}
			return spent * 100 / target
		},
		// utc renders a timestamp for a <time> element: the machine-readable
		// value for its datetime attribute, and a labelled UTC text the page
		// script (src/ts/localtime.ts) replaces with local time. Without the
		// script the label keeps the UTC value from being misread as local.
		"utc": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format(time.RFC3339)
		},
		"utcText": func(t time.Time) string {
			if t.IsZero() {
				return "never"
			}
			return t.UTC().Format("2 Jan 2006 15:04") + " UTC"
		},
	}
}

// actionLabels turn audit log action codes into words for the activity page.
var actionLabels = map[string]string{
	"category.create":  "Created category",
	"category.update":  "Changed category",
	"category.archive": "Archived a category",
	"category.restore": "Restored a category",
	"category.starter": "Added the starter categories",
	"rule.create":      "Created rule",
	"rule.update":      "Changed rule",
	"rule.delete":      "Deleted rule",
	"rule.apply":       "Applied a rule to",
	"target.set":       "Set a target",
	"target.copy":      "Copied targets from",
	"account.update":   "Changed account",
}

func actionLabel(code string) string {
	if l, ok := actionLabels[code]; ok {
		return l
	}
	return code
}

// renderPartial executes one partial on its own, for an htmx swap into a page
// already on screen. Every page set carries the partials, so any one serves. Like
// render, it buffers first so a template error can still be a 500.
func (h *Handler) renderPartial(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := h.tmpls["home.html"].ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("render partial %s: %v", name, err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("write partial %s: %v", name, err)
	}
}

// render executes a page through its layout into a buffer first, so a template
// error is still answerable with a 500 rather than a committed 200 and half a page.
func (h *Handler) render(w http.ResponseWriter, status int, page string, data any) {
	set, ok := h.tmpls[page]
	if !ok {
		log.Printf("render: no such page %q", page)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("write %s: %v", page, err)
	}
}

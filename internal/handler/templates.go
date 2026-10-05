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
	"time"
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
		"asset": h.assetURL,
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

package handler

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/middleware"
)

// Bundled assets: the stylesheet, a vendored htmx (see static/README.md) and
// dist/, which `deno task build` writes from src/ts before `go build` runs.
//
// There is deliberately no STATIC_DIR override: this serves one household, not
// adopters restyling without forking.
//
//go:embed static
var staticFS embed.FS

// contentTypes maps an extension to what the file is served as. An explicit map
// rather than mime.TypeByExtension: an extension not listed here is not served at
// all, so a stray file under static/ cannot be published by accident. That is
// also why static/README.md is embedded but unreachable.
var contentTypes = map[string]string{
	".css":     "text/css; charset=utf-8",
	".js":      "text/javascript; charset=utf-8",
	".svg":     "image/svg+xml",
	".png":     "image/png",
	".ico":     "image/x-icon",
	".LICENSE": "text/plain; charset=utf-8",
}

// requiredAssets must exist, or the server refuses to start. dist/app.js is the
// one at risk: it is build output, so a `go build` without `deno task build`
// first embeds an empty dist/ and every page would quietly lose its script.
var requiredAssets = []string{"styles.css", "htmx.min.js", "dist/app.js"}

type staticAsset struct {
	body        []byte
	contentType string
	etag        string
	url         string
}

// loadAssets reads the embedded static tree once, hashing each file so every
// rendered page can reference it by a content-addressed URL.
func loadAssets() (map[string]staticAsset, error) {
	out := make(map[string]staticAsset)
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := strings.TrimPrefix(p, "static/")
		ext := path.Ext(name)
		contentType, ok := contentTypes[ext]
		if !ok {
			// .md (the vendoring record) and .map (sourcemaps) are embedded
			// but deliberately unserved; anything else is a file someone added
			// without deciding whether it should be public.
			if ext == ".md" || ext == ".map" {
				return nil
			}
			return fmt.Errorf("static asset %s has no content type; add its extension to contentTypes or remove the file", name)
		}
		body, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])[:12]
		out[name] = staticAsset{
			body:        body,
			contentType: contentType,
			etag:        `"` + digest + `"`,
			url:         middleware.StaticPrefix + name + "?v=" + digest,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load embedded static: %w", err)
	}
	for _, name := range requiredAssets {
		if _, ok := out[name]; !ok {
			return nil, fmt.Errorf("embedded asset %s is missing: run `deno task build` before `go build` (make build does both)", name)
		}
	}
	return out, nil
}

// assetURL resolves a bundled file to its content-addressed URL for the {{asset}}
// template func. A missing name becomes a visibly wrong URL rather than an empty
// attribute, so a typo shows as a 404 in the network tab.
func (h *Handler) assetURL(name string) string {
	a, ok := h.assets[name]
	if !ok {
		return middleware.StaticPrefix + "missing/" + name
	}
	return a.url
}

// ServeStatic serves one bundled asset. The URL carries a hash of the file's own
// contents, which is the only thing that makes an immutable cache honest.
func (h *Handler) ServeStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	a, ok := h.assets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("ETag", a.etag)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.body))
}

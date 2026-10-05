# Bundled assets

Everything here is embedded by `//go:embed static` in `internal/handler/static.go`
and served from `/static/`, which is why the CSP needs nothing beyond `'self'`.

`.md` and `.map` are deliberately absent from the content-type allowlist in
`static.go`, so this file (and any sourcemap) is embedded but not reachable over
HTTP.

## dist/

Build output from `deno task build` (TypeScript in `src/ts/`). Ignored by git apart
from `.gitkeep`. The server refuses to start if `dist/app.js` is missing, because a
`go build` without the Deno step would otherwise ship pages with no script and no
error.

## htmx 2.0.10

- Source: `https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.10.tgz`, `package/dist/htmx.min.js`
- Licence: Zero-Clause BSD (`htmx.LICENSE`)
- SHA-256: `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de`

Copied from website-backend, where the bytes were checked against the npm tarball's
published `integrity` hash rather than taken from a CDN on trust. The SHA-256 above
was re-checked on copying.

To upgrade, verify the same way:

```sh
V=2.0.11
curl -sSO "https://registry.npmjs.org/htmx.org/-/htmx.org-$V.tgz"
# compare sha512 of the tarball against .dist.integrity from
#   https://registry.npmjs.org/htmx.org/$V
tar -xzOf "htmx.org-$V.tgz" package/dist/htmx.min.js > htmx.min.js
sha256sum htmx.min.js   # record it above
```

## The htmx indicator rules live in styles.css

htmx injects its `.htmx-indicator` rules as a `<style>` block, which
`style-src 'self'` blocks, and it fails *open*: the rule that hides an indicator
never applies. So the injection is off (`includeIndicatorStyles:false` in the
`htmx-config` meta tag in `templates/partials/document.html`) and the rules live at
the bottom of `styles.css`. Keep both, or drop both.

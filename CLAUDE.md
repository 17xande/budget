# Budget

Household budgeting app for two people: FNB transactions in, categorised spending
against monthly targets out. Read README.md for the decisions and docs/fnb-flow.md
before touching anything FNB.

Load the `go-htmx-web` skill whenever working here; it is the house style this
project follows.

## Commands

```sh
make run            # bundle TS, then serve http://localhost:8080 (COOKIE_SECURE=false)
make test           # THE gate: gofmt, vet, go test -count=1, deno check/test/fmt/lint
make build          # bundle + bin/budget
make recreate-db    # schema change while greenfield
docker build -t budget . && docker run --rm -p 127.0.0.1:18080:8080 -e COOKIE_SECURE=false budget
```

## Traps

- **`dist/app.js` is build output that `go:embed` needs at compile time.** Run
  `deno task build` (or any make target) before `go build`/`go test`, or the
  handler refuses to start with a message saying so. Never commit dist/.
- **No `style=` and no `on*=` in templates.** The CSP is `'self'` with no
  `'unsafe-inline'`; an inline handler renders, returns 200, passes tests and does
  nothing in a browser. `TestNoInlineStylesOrHandlers` enforces it. Behaviour goes
  in `src/ts`, styling in `static/styles.css`.
- **New protected routes go through `route()` in handler.go.** A direct
  `mux.Handle` is unprotected; `TestPublicRoutesPinned` fails on one.
- **CSRF tokens in tests must be HTML-unescaped** (base64 `+` renders as `&#43;`).
  The test helper does it; a hand-rolled scrape will fail one run in a few.
- **Times are UTC RFC3339 TEXT.** Render with the `time` partial, which labels UTC
  and lets `src/ts/localtime.ts` localise it. Never show an unlabelled UTC time.
- **STRICT cannot be retrofitted**, and there is no startup ALTER by design.
- **Never put real bank data in the repo.** Fixtures are synthetic; .gitignore
  blocks csv/ofx/zip outside testdata, but check what you add.
- **FNB login must never spoof the browser** (user agent, fingerprint, Radware
  tokens). If headless Chrome is refused, stop and report; do not evade.

## Verifying UI changes

Drive it with the Chrome DevTools MCP server in an isolated context, on a fresh
port: interact with what changed, read the console (CSP violations appear only
there), and check 390px width for horizontal body scroll.

# Budget

A household budget for two people. Transactions come from FNB (South Africa),
are categorised by rules, and are compared against monthly targets per category.

Go + htmx + a little TypeScript (bundled by Deno), SQLite, one static binary in a
distroless container behind Traefik.

## Status

| Phase | What | State |
|---|---|---|
| 0 | Capture FNB's export flow; prove automated login is possible | done — see [docs/fnb-flow.md](docs/fnb-flow.md) |
| 1 | Scaffold: server, database, sign-in for two people, build, container | done |
| 2 | Ledger + manual CSV/OFX import | done |
| 3 | Categories, rules, monthly targets, dashboard | done |
| 4 | MCP server: AI reads the budget and categorises, with tokens and an undoable activity log | done — see [docs/mcp.md](docs/mcp.md) |
| 5 | Daily automatic FNB fetch | next |
| 6 | Backups, export, hardening | |

## Running it

Development, on the host (needs Go and Deno):

```sh
make run          # bundles the TypeScript, then http://localhost:8080
```

The first start prints a one-time **setup token** in the log. Open the app, paste
it with your email and a password, and you are signed in. Add the second person
from **Members** with a temporary password; their first sign-in makes them choose
their own.

Then **Import** FNB downloads (CSV, OFX, or the ZIPs as they arrive), as many
and as overlapping as you like: each transaction is stored once. Add the starter
**categories** (or your own), make **rules** from transactions ("contains
CHECKERS → Groceries"; new imports are categorised as they arrive), set monthly
**targets**, and the **overview** shows each month's spending against them.

`make test` is the gate: gofmt, `go vet`, uncached Go tests, and Deno
check/test/fmt/lint.

Production: copy `.env.example` to `.env`, set `BUDGET_HOST`, then
`docker compose up -d --build`. It joins the external `traefik_proxy` network like
the other stacks and stores the database on the `budget-data` volume. The setup
token is in `docker compose logs budget`.

Locked out:

```sh
make reset-password EMAIL=you@example.com                 # local database
# in the container: the password goes on stdin, never as an argument
read -rs P && printf '%s\n' "$P" | docker compose exec -T budget /budget reset-password you@example.com
```

## Layout

```
main.go                 wiring, graceful shutdown, `reset-password` subcommand
setup_token.go          first-boot setup token
internal/config         all environment parsing
internal/db             SQLite open + schema (STRICT tables)
internal/auth           argon2id passwords, sessions, setup claim
internal/importer       FNB CSV / OFX / ZIP parsers (pure; fixture-tested)
internal/ledger         accounts, transactions, import history
internal/budget         categories, rules, targets, the month summary, audit log
internal/money          exact decimal <-> cents
internal/mcpserver      the /mcp endpoint for AI clients (docs/mcp.md)
internal/validate       form validation shared by the web forms and the CLI
internal/middleware     security headers/CSP, session enforcement, rate limits
internal/handler        routes, pages, templates/, static/ (all go:embed'ed)
src/ts                  TypeScript, bundled to internal/handler/static/dist
docs/                   the FNB flow
```

## Decisions

**Dependencies.** The objection is to frameworks, not libraries. Every direct
dependency, and why:

| Dependency | For | Why not the alternative |
|---|---|---|
| `modernc.org/sqlite` | SQLite driver | Pure Go, so `CGO_ENABLED=0` and a distroless/static image with no libc or shell. `mattn/go-sqlite3` is smaller and canonical, but needs cgo and an Alpine-class runtime. modernc is a large machine-translated module; accepted for the static image, which matters more here because the container will hold bank credentials. |
| `golang.org/x/crypto` | argon2id | Password hashing is not something to hand-roll. |
| `justinas/nosurf` | CSRF tokens and origin checks | Same. |
| `golang.org/x/time/rate` | token buckets for the rate limits | Small, from the Go team. |
| `modelcontextprotocol/go-sdk` | the MCP endpoint (phase 4) | The official SDK. The protocol is still moving; hand-rolling it means owning that churn, and the SDK infers tool schemas from Go structs. Already proven in gostore. |

Deliberately not taken: a router (the stdlib mux does methods and wildcards), a
validation library, an ORM or sqlc (a handful of hand-written typed queries), a
CSS framework, a chart library (charts will be server-rendered SVG), web fonts.
Phase 5 adds `chromedp` for the FNB login; see below.

**SQLite, one instance.** It changes to Postgres only if a second app instance or
another process needs the database. STRICT tables throughout; no migrations while
greenfield (`make recreate-db`) — adopt goose once there is real data worth
keeping. Money will be integer cents.

**Auth.** Two named accounts, no roles. Sessions are random tokens stored as
sha256 in a table, so a password change ends every session. The first account is
claimed with a one-time token printed at boot, never a default password.

**Security baseline.** A single CSP of `'self'` with no `'unsafe-inline'`, so no
template may carry a `style=` or `on*=` attribute (a test enforces it). Session
cookie HttpOnly, `SameSite=Lax`, Secure unless `COOKIE_SECURE=false`. Pages are
`Cache-Control: no-store`. Every route that checks a secret is rate limited per IP,
and failed sign-ins additionally per email address. No HSTS from the app;
Traefik's job.

**FNB.** No API exists for personal customers. A headless Chromium logs in
through FNB's real page (it runs FNB's bot-protection script as designed; nothing
is spoofed), then plain `net/http` replays the export requests with the session
cookies. Proven end to end in Phase 0. If FNB ever blocks the headless browser, we
stop and fall back to manual upload rather than evade it.

## Decisions still open

| Decision | Candidates | When it is forced |
|---|---|---|
| Which FNB accounts to track | all / chosen in settings (loan and savings probably not "spending") | Phase 5 |
| Fetch-failure notification | status page only / email / ntfy push | Phase 5 |
| inContact email feed (IMAP) for same-day transactions | add / never | after Phase 5 |
| An MCP tool to trigger the FNB fetch | no for now: it would let an AI cause a bank login | only if asked, after Phase 5 |
| WAL mode | off / on (changes the backup command) | Phase 6 |
| Off-host backups | none / R2 | Phase 6 |
| CI | none (no remote yet) / GitHub Actions | when the repo gets a remote |

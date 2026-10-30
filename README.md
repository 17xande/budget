# Budget

A household budget for two people. Transactions come from FNB (South Africa),
are categorised by rules, and are compared against monthly targets per category.

Go + htmx + a little TypeScript (bundled by Deno), SQLite, one static binary in a
distroless container behind Traefik.

The interface is the "Ledger" design: IBM Plex, one stylesheet of plain CSS, a segmented
nav, panels and flat tables, light or dark following the system setting (no toggle).
Wide tables (transactions, import history, tokens, activity, fetch runs) become cards on a phone; the others fit or scroll inside their panel. Every page works without JavaScript.

## Status

| Phase | What | State |
|---|---|---|
| 0 | Capture FNB's export flow; prove automated login is possible | done — see [docs/fnb-flow.md](docs/fnb-flow.md) |
| 1 | Scaffold: server, database, sign-in for two people, build, container | done |
| 2 | Ledger + manual CSV/OFX import | done |
| 3 | Categories, rules, monthly targets, dashboard | done |
| 4 | MCP server: AI reads the budget and categorises, with tokens and an undoable activity log | done — see [docs/mcp.md](docs/mcp.md) |
| 5 | Daily automatic FNB fetch | done — needs your credentials to switch on, see below |
| 6 | Backups, export, hardening | done |
| 7 | Restyle to the "Ledger" design: shell, tokens, light/dark, then every page (all four steps) | done |

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

`make test` is the gate: gofmt, `go vet`, uncached Go tests under `-race` (with a
count of skipped tests), Deno check/test/fmt/lint, then `govulncheck` (pinned in
the Makefile; needs network).

Production is deployed with [Kamal](https://kamal-deploy.org) (`config/deploy.yml`)
to the Docker host `home-docker`. The one-time host setup (kamal-proxy and the
shared Cloudflare tunnel) is done by `~/dev/17xande.dev/infra`.

- **Deploy:** `make deploy` runs the test gate, then `kamal deploy`: it builds the
  image locally, pushes it to `ghcr.io/17xande-dev/budget` and swaps it in once
  `/health` passes. It logs in to ghcr.io with `gh auth token` (`.kamal/secrets`).
- **Roll back:** `kamal rollback <version>` (`kamal app details` lists versions).
- **Logs:** `make logs-prod` (`kamal app logs -f`).
- **Hostname:** in the shared tunnel in Cloudflare, add a public hostname
  `budget.17xande.dev` with service `http://kamal-proxy:80`.
- The tunnel is the only way in and nothing is published on the host, so the app
  runs with `CLIENT_IP_SOURCE=cloudflare` (rate limits read `CF-Connecting-IP`).
  The database lives on the existing `budget_budget-data` Docker volume. The daily
  FNB fetch is not part of the deployment yet.

**`compose.yaml` behind Traefik** is the other way to run it. Copy `.env.example`
to `.env`, set `BUDGET_HOST`, then `docker compose up -d --build`. It joins the
external `traefik_proxy` network and stores the database on the `budget-data`
volume. Leave `CLIENT_IP_SOURCE` unset there: Traefik passes a client-sent
`CF-Connecting-IP` through, so trusting it would let a client choose its own
rate-limit bucket. The cost is that every client shares one bucket.

The setup token is in `docker compose logs budget` (or the host's equivalent).

### The daily FNB fetch

Off until configured. To switch it on:

```sh
mkdir -p secrets && chmod 700 secrets
read -rs P && printf '%s\n' "$P" > secrets/fnb_password && chmod 600 secrets/fnb_password
```

then in `.env` set `FNB_USERNAME`, and `FNB_PROFILE` if your login shows a "Linked
Profiles" page (the exact name of the personal profile). `docker compose up -d`
starts the app and a headless Chrome beside it. **Settings → Bank fetch** shows the
next run and every attempt; **Fetch now** runs one immediately (at most every 10
minutes). It fetches cheque (`DDA`) and credit-card (`ZFN`) accounts by default
(`FNB_PRODUCTS`); switch any account off under **Settings → Accounts**.

It tries once a day (06:00 South African time, `FETCH_AT`) and **never retries a
failure on its own** — repeated failed logins can lock a banking profile. A failed
fetch shows as a banner on the overview until the next one succeeds.

Locked out:

```sh
make reset-password EMAIL=you@example.com                 # local database
# in the container: the password goes on stdin, never as an argument
read -rs P && printf '%s\n' "$P" | docker compose exec -T budget /budget reset-password you@example.com
```

### Backups and export

A copy of the database is made at startup and then once a day, into `backups/`
beside it (`/data/backups` in the container, so inside the same volume), and the
newest 14 are kept (`BACKUP_DIR`, `BACKUP_KEEP`; `0` turns them off). Each is a
complete database made with `VACUUM INTO`, consistent even while the app is writing.
**Settings** shows the latest and warns when it is over two days old.

They sit on the same disk, so they undo a mistake, not a lost machine. Copy them off
now and then:

```sh
docker compose cp budget:/data/backups ./budget-backups
```

To restore one, stop the app and copy it over the database inside the volume (the
image has no shell, so a throwaway busybox does the copy; overwriting keeps the
file's owner, the app's non-root user):

```sh
docker compose stop budget
docker run --rm --volumes-from "$(docker compose ps -aq budget)" busybox \
  sh -c 'cp /data/backups/budget-2026-10-05.db /data/budget.db && chown 65532:65532 /data/budget.db'
docker compose start budget
```

**Transactions → Download these as CSV** exports what the page is showing;
**Settings → Export** downloads everything. Text cells that a spreadsheet would run as
a formula (`=`, `+`, `-`, `@`) are prefixed with `'`.

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
internal/fnb            FNB online banking: Chrome login, HTTP export (fnbtest: a fake bank)
internal/fetch          the daily fetch: guards, runs, import, status
internal/validate       form validation shared by the web forms and the CLI
internal/middleware     security headers/CSP, session enforcement, rate limits
internal/handler        routes, pages, templates/, static/ (all go:embed'ed)
src/ts                  TypeScript, bundled to internal/handler/static/dist
docs/                   the FNB flow (fnb-flow.md), the MCP endpoint (mcp.md)
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
| `chromedp/chromedp` (+ `chromedp/remote`) | driving headless Chrome for the FNB login (phase 5) | FNB's login is behind bot protection that only a real browser passes honestly. chromedp's core needs only the stdlib and cdproto; the websocket half is a separate small module. Chosen over hand-rolled CDP. |
| `modelcontextprotocol/go-sdk` | the MCP endpoint (phase 4) | The official SDK. The protocol is still moving; hand-rolling it means owning that churn, and the SDK infers tool schemas from Go structs. Already proven in gostore. |

Deliberately not taken: a router (the stdlib mux does methods and wildcards), a
validation library, an ORM or sqlc (a handful of hand-written typed queries), a
CSS framework, a chart library (charts will be server-rendered SVG).
`chromedp` drives the FNB login; see below.

**Web fonts, from Google.** The look (IBM Plex Sans and Mono) is loaded from Google
Fonts by a `<link>` in the layout head, kept off our own server by choice. The CSP
names exactly two extra hosts: `https://fonts.googleapis.com` in `style-src` and
`https://fonts.gstatic.com` in `font-src`; no script, no connection and no bank data
goes to either, and a test pins that those are the only external origins. The cost is
privacy: Google sees a page view from each browser that opens the app. The font stacks
keep `system-ui` / `ui-monospace` fallbacks, so the app works fully if the host is
blocked or unreachable.

**SQLite, one instance.** It changes to Postgres only if a second app instance or
another process needs the database. STRICT tables throughout; no migrations while
greenfield (`make recreate-db`) — adopt goose once there is real data worth
keeping. Money will be integer cents.

**Auth.** Two named accounts, no roles. Sessions are random tokens stored as
sha256 in a table, so a password change ends every session. The first account is
claimed with a one-time token printed at boot, never a default password.

**Security baseline.** A single CSP that is `'self'` except for the two font hosts (`https://fonts.googleapis.com` in `style-src` only, `https://fonts.gstatic.com` in `font-src` only), with no `'unsafe-inline'`, so no
template may carry a `style=` or `on*=` attribute (a test enforces it). Session
cookie HttpOnly, `SameSite=Lax`, Secure unless `COOKIE_SECURE=false`. Pages are
`Cache-Control: no-store`. Every route that checks a secret is rate limited per IP,
and failed sign-ins additionally per email address. IPv6 clients are keyed by their /64,
each limiter holds at most 10000 buckets and refuses newcomers when full, and argon2
runs at most two at a time (each costs 64 MiB). No HSTS from the app;
Traefik's job.

**FNB.** No API exists for personal customers. A headless Chromium logs in
through FNB's real page (it runs FNB's bot-protection script as designed; nothing
is spoofed), then plain `net/http` replays the export requests with the session
cookies. Proven end to end in Phase 0. If FNB ever blocks the headless browser, we
stop and fall back to manual upload rather than evade it.

## Decisions still open

| Decision | Candidates | When it is forced |
|---|---|---|
| Fetch-failure notification | now: status page + overview banner; later: email or ntfy push | if a failure goes unnoticed in practice |
| inContact email feed (IMAP) for same-day transactions | add / never | after Phase 5 |
| An MCP tool to trigger the FNB fetch | no for now: it would let an AI cause a bank login | only if asked, after Phase 5 |
| WAL mode | off (SQLite's default journal) / on. Two people never contend, so nothing forces it; backups use `VACUUM INTO`, which works the same either way | if writes ever wait on a long read (the fetch, an export) |
| Off-host backups | by hand for now (`docker compose cp`, above) / a scheduled push to R2 or another machine | before the data is worth more than a re-import of the last three months |
| CI | none: the remote is public but there is no hosting CI; `make test` (with `-race` and `govulncheck`) is the gate / GitHub Actions | if a run on every push is wanted |

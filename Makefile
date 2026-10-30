# Host-side development targets. The container builds itself (see Dockerfile).
.DEFAULT_GOAL := help
DB_PATH ?= data/budget.db

# govulncheck is pinned here rather than in go.mod, so it is not a dependency of
# the module. Older releases crash on Go 1.27, so do not lower this.
GOVULNCHECK_VERSION ?= v1.9.0

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'

## bundle: build the TypeScript into internal/handler/static/dist (go:embed needs it)
bundle:
	deno task build

## run: bundle, then run the server on http://localhost:8080 with a local database
run: bundle
	@mkdir -p $(dir $(DB_PATH))
	COOKIE_SECURE=false DB_PATH=$(DB_PATH) go run .

## build: bundle, then build bin/budget
build: bundle
	CGO_ENABLED=0 go build -trimpath -o bin/budget .

## test: the full gate -- format, vet, Go tests (uncached, -race, skips counted), Deno check and tests, then vulncheck
test: bundle
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	@out=$$(mktemp); \
	  go test -count=1 -race -v ./... >$$out 2>&1 || { cat $$out; rm -f $$out; exit 1; }; \
	  grep -E '^(ok|\?) ' $$out; \
	  echo "skipped tests: $$(grep -cE '^ *--- SKIP' $$out)"; grep -E '^ *--- SKIP' $$out || true; \
	  rm -f $$out
	deno task check
	deno task test
	deno fmt --check src/ts deno.json
	deno lint src/ts
	$(MAKE) vulncheck

## vulncheck: fail if a reachable known vulnerability affects the build (needs network)
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

## deploy: test, build the image, push it to ghcr.io, pull and restart it on home-docker
deploy:
	./scripts/deploy.sh

## fmt: format Go and TypeScript
fmt:
	gofmt -w .
	deno fmt src/ts deno.json

## tidy: go mod tidy
tidy:
	go mod tidy

## reset-password: set EMAIL's password on the local database (lockout recovery)
reset-password:
	@test -n "$(EMAIL)" || { echo "usage: make reset-password EMAIL=you@example.com"; exit 1; }
	@read -rsp "New password for $(EMAIL): " P; echo; \
	  printf '%s\n' "$$P" | DB_PATH=$(DB_PATH) go run . reset-password "$(EMAIL)"

## recreate-db: DELETE the local database. The schema-change path while greenfield.
recreate-db:
	rm -f $(DB_PATH) $(DB_PATH)-wal $(DB_PATH)-shm

## image: build the container image
image:
	docker compose build

## up: build and start the stack (needs .env and the traefik_proxy network)
up:
	docker compose up -d --build

## logs: follow the app's logs
logs:
	docker compose logs -f budget

.PHONY: help bundle run build test vulncheck fmt tidy reset-password recreate-db image up logs

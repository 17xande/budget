# Host-side development targets. The container builds itself (see Dockerfile).
.DEFAULT_GOAL := help
DB_PATH ?= data/budget.db

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

## test: the full gate -- format, vet, Go tests (uncached), Deno check and tests
test: bundle
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test -count=1 ./...
	deno task check
	deno task test
	deno fmt --check src/ts deno.json
	deno lint src/ts

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

.PHONY: help bundle run build test fmt tidy reset-password recreate-db image up logs

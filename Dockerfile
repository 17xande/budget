# Bundle the TypeScript, build a static binary that embeds it, then ship the
# binary alone. Templates and assets are go:embed'ed, so the runtime image has
# no files and needs no shell.

FROM denoland/deno:alpine-2.9.6 AS frontend
WORKDIR /app
COPY deno.json deno.lock ./
COPY src/ ./src/
# Populate the module cache from the lockfile first, so a bad or missing entry
# fails loudly instead of silently refetching.
RUN deno install --frozen
RUN deno task build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# go:embed reads dist/ at compile time, so the bundle must land before go build.
COPY --from=frontend /app/internal/handler/static/dist/ ./internal/handler/static/dist/
# CGO off is the point of modernc.org/sqlite: a static binary for distroless/static.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/budget .
# The database directory, created with the runtime user's ownership. Distroless
# has no shell to mkdir at start-up, and Docker seeds a fresh named volume from
# whatever the image has at the mount path, ownership included.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/budget /budget
# 65532 is distroless's nonroot uid.
COPY --from=build --chown=65532:65532 /out/data /data
ENV DB_PATH=/data/budget.db PORT=8080
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/budget"]

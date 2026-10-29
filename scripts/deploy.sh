#!/usr/bin/env bash
# Build the image, publish it to GitHub's container registry, then tell the Docker
# host to pull that exact tag and restart on it. The host never builds and never
# sees the source: it needs only compose.prod.yaml and its own .env.
#
#   make deploy
#
# Needs: docker with buildx, the gh CLI signed in with the write:packages scope,
# and ssh access to $DEPLOY_HOST. A clean, committed tree, so the tag (the commit)
# names exactly what runs.
set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/17xande/budget}"
DEPLOY_HOST="${DEPLOY_HOST:-home-docker}"
DEPLOY_DIR="${DEPLOY_DIR:-budget}"
URL="${DEPLOY_URL:-https://budget.17xande.dev}"

cd "$(dirname "$0")/.."

# Tracked files only: an untracked scratch file (AGENTS.md, say) is not code that
# ships, and .dockerignore keeps stray files out of the image anyway.
if ! git diff --quiet HEAD --; then
	echo "deploy: tracked files have uncommitted changes; commit first so the tag names what runs" >&2
	exit 1
fi
TAG="$(git rev-parse --short=12 HEAD)"

echo "==> test gate"
make test

echo "==> build $IMAGE:$TAG"
# The host is linux/amd64. Building for it explicitly keeps an arm laptop honest.
docker buildx build --platform linux/amd64 --load -t "$IMAGE:$TAG" -t "$IMAGE:latest" .

echo "==> push to ghcr.io"
gh auth token | docker login ghcr.io -u "$(gh api user --jq .login)" --password-stdin >/dev/null
docker push "$IMAGE:$TAG"
docker push "$IMAGE:latest"

echo "==> deploy $TAG to $DEPLOY_HOST"
scp -q compose.prod.yaml "$DEPLOY_HOST:$DEPLOY_DIR/compose.prod.yaml"
# TAG lives in the host's .env so a later restart or reboot keeps running this
# release rather than whatever :latest has moved to.
ssh "$DEPLOY_HOST" "set -e; cd $DEPLOY_DIR
	touch .env; chmod 600 .env
	if grep -q '^TAG=' .env; then sed -i 's/^TAG=.*/TAG=$TAG/' .env; else echo 'TAG=$TAG' >> .env; fi
	docker compose -f compose.prod.yaml pull budget
	docker compose -f compose.prod.yaml up -d --remove-orphans
	docker image prune -f >/dev/null"

echo "==> wait for $URL"
for i in $(seq 1 30); do
	code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$URL/login" || true)"
	case "$code" in 200|303) echo "up ($code): $TAG is live"; exit 0;; esac
	sleep 2
done
echo "deploy: $URL did not answer 200/303 within a minute (last: ${code:-none}); check: ssh $DEPLOY_HOST 'cd $DEPLOY_DIR && docker compose -f compose.prod.yaml logs --tail 50'" >&2
exit 1

#!/usr/bin/env bash
# Smoke of a built image: start it against a throwaway Postgres and require /healthz
# to answer 200 and the app shell to be served. The arm64 image runs under QEMU on the
# amd64 runner. Usage: bash scripts/image-smoke.sh <image>
set -euo pipefail

image="${1:?usage: image-smoke.sh <image>}"
net="smoke-$$"
cleanup() {
  docker logs "$net-app" 2>&1 | tail -30 || true
  docker rm -f "$net-app" "$net-db" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$net" >/dev/null
docker run -d --name "$net-db" --network "$net" -e POSTGRES_PASSWORD=smoke postgres:16 >/dev/null
for _ in $(seq 1 30); do
  docker exec "$net-db" pg_isready -U postgres >/dev/null 2>&1 && break
  sleep 2
done

docker run -d --name "$net-app" --network "$net" --platform linux/arm64 -p 127.0.0.1:8080:8080 \
  -e DRAW_DATABASE_URL="postgres://postgres:smoke@$net-db:5432/postgres" \
  -e DRAW_SECRET=0123456789abcdef0123456789abcdef \
  "$image" >/dev/null

for _ in $(seq 1 60); do
  sleep 3
  if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8080/healthz || true)" = "200" ]; then
    curl -fsS --max-time 5 http://127.0.0.1:8080/ | grep -q '<div id="root">' || { echo "app shell missing" >&2; exit 1; }
    # Without the identity header the workspace API must refuse.
    [ "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/me)" = "401" ] || { echo "/api/me is open" >&2; exit 1; }
    echo "image is healthy"
    exit 0
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$net-app" 2>/dev/null)" != "true" ]; then
    echo "container exited before becoming healthy" >&2
    exit 1
  fi
done
echo "healthz did not answer 200 in time" >&2
exit 1

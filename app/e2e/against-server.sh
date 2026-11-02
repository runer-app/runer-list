#!/usr/bin/env bash
# Runs the app's e2e suite against the real server: a throwaway PostgreSQL in Docker and the server from server/,
# on ports of their own so a running dev stack is left alone. `make test` runs it.
set -euo pipefail
cd "$(dirname "$0")/../.."
TMPDIR=${TMPDIR:-/tmp}

DB_PORT=${DB_PORT:-55432}
PORT=${PORT:-18080}
URL="http://127.0.0.1:$PORT"
DB=runer-e2e-$$

docker run -d --rm --name "$DB" -p "127.0.0.1:$DB_PORT:5432" \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=runer postgres:18-alpine >/dev/null
SERVER=
cleanup() {
  [ -n "$SERVER" ] && kill "$SERVER" 2>/dev/null || true
  docker rm -f "$DB" >/dev/null 2>&1 || true
}
trap cleanup EXIT

until docker exec "$DB" pg_isready -U postgres -d runer >/dev/null 2>&1; do sleep 0.5; done

(cd server && go build -o "$TMPDIR/runer-e2e-server" ./cmd/server)
# The limits are per IP and every test account comes from localhost: lift them, and make the proof-of-work cheap.
DATABASE_URL="postgres://postgres:postgres@127.0.0.1:$DB_PORT/runer?sslmode=disable" \
  ORIGIN=tauri://localhost PUBLIC_URL="$URL" LISTEN_ADDR="127.0.0.1:$PORT" \
  SIGNUP_POW_BITS=8 CHALLENGES_PER_IP_PER_MINUTE=1000000 SIGNUPS_PER_IP_PER_HOUR=1000000 \
  SIGNUP_BURST_PER_IP=1000000 INVITE_READS_PER_IP_PER_MINUTE=1000000 \
  "$TMPDIR/runer-e2e-server" >"$TMPDIR/runer-e2e-server.log" 2>&1 &
SERVER=$!

for _ in $(seq 60); do curl -sf "$URL/healthz" >/dev/null && break; sleep 0.5; done
curl -sf "$URL/healthz" >/dev/null || { cat "$TMPDIR/runer-e2e-server.log"; exit 1; }

RUNER_E2E_SERVER="$URL" npm run test:e2e --prefix app

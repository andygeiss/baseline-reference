#!/bin/sh
# Reproducible verification of the baseline reference implementation.
# Runs every mechanical gate from the baseline's operations/ci.md, then boots the
# real binary and smoke-tests the running application. Exits non-zero on any failure.
#
# Usage: ./verify.sh [project-dir]   (defaults to the directory of this script)
set -eu

cd "${1:-$(dirname "$0")}"

# Pinned in the baseline's VERSIONS.md; update both together.
HTMX_SHA256="71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de"

PORT="${PORT:-8091}"
OPS_PORT="${OPS_PORT:-6061}"
WORKDIR="$(mktemp -d)"
SERVER_PID=""
cleanup() {
    [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
    rm -rf "$WORKDIR"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

step "gofmt"
[ -z "$(gofmt -l .)" ] || { gofmt -l .; fail "gofmt"; }

step "go vet"
go vet ./...

step "staticcheck"
go run honnef.co/go/tools/cmd/staticcheck@latest ./...

step "govulncheck"
go run golang.org/x/vuln/cmd/govulncheck@latest ./...

step "go mod tidy -diff"
go mod tidy -diff

step "tests (race, shuffled)"
go test -race -shuffle=on ./...

step "vendored htmx checksum"
if command -v sha256sum >/dev/null 2>&1; then
    echo "$HTMX_SHA256  web/static/js/htmx.min.js" | sha256sum -c -
else
    echo "$HTMX_SHA256  web/static/js/htmx.min.js" | shasum -a 256 -c -
fi

step "static build (CGO_ENABLED=0, trimpath)"
CGO_ENABLED=0 go build -trimpath -o "$WORKDIR/server" ./cmd/server

step "smoke test: booting server on :$PORT (ops :$OPS_PORT)"
"$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -db "$WORKDIR/app.db" \
    >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS "localhost:$OPS_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done

step "smoke: health endpoint reports ok"
curl -fsS "localhost:$OPS_PORT/healthz" | grep -q '"status":"ok"' || fail "healthz"

step "smoke: home page with CSP header"
curl -fsS -D "$WORKDIR/headers" "localhost:$PORT/" | grep -q "Start a new game" || fail "home body"
grep -qi "content-security-policy: default-src 'self'; frame-ancestors 'none'" "$WORKDIR/headers" || fail "CSP header"
grep -qi "strict-transport-security" "$WORKDIR/headers" || fail "HSTS header"

step "smoke: install manifest linked, served as application/manifest+json"
curl -fsS "localhost:$PORT/" | grep -q 'rel="manifest"' || fail "manifest not linked from layout"
curl -fsS -D "$WORKDIR/mheaders" "localhost:$PORT/static/manifest.webmanifest" \
    | grep -q '"start_url": "/"' || fail "manifest body"
# Go's built-in mime table has no .webmanifest entry and the Unix mime files it
# merges vary by host: this gate proves main.go's AddExtensionType is doing it.
grep -qi "content-type: application/manifest+json" "$WORKDIR/mheaders" || fail "manifest content-type"

step "smoke: install icons served"
for ICON in icon-192.png icon-512.png icon-512-maskable.png apple-touch-icon.png; do
    curl -fsS -o /dev/null "localhost:$PORT/static/$ICON" || fail "icon missing: $ICON"
done

step "smoke: plain form flow (create game -> 303 -> page)"
GAME_URL="$(curl -fsS -o /dev/null -w '%{redirect_url}' -X POST "localhost:$PORT/games")"
case "$GAME_URL" in */games/*) ;; *) fail "create redirect: $GAME_URL";; esac
curl -fsS "$GAME_URL" | grep -q 'id="board"' || fail "game page"

step "smoke: plain move is POST-redirect-GET"
CODE="$(curl -fsS -o /dev/null -w '%{http_code}' -X POST "$GAME_URL/moves" -d cell=0)"
[ "$CODE" = "303" ] || fail "plain move: got $CODE, want 303"

step "smoke: htmx move returns board fragment, not a page"
FRAGMENT="$(curl -fsS -X POST "$GAME_URL/moves" -H 'HX-Request: true' -d cell=4)"
echo "$FRAGMENT" | grep -q 'id="board"' || fail "fragment missing board"
echo "$FRAGMENT" | grep -q '<html' && fail "fragment is a full page"

step "smoke: cross-site POST rejected (CSRF)"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$GAME_URL/moves" \
    -H 'Sec-Fetch-Site: cross-site' -d cell=1)"
[ "$CODE" = "403" ] || fail "CSRF: got $CODE, want 403"

step "smoke: state survives restart"
kill -TERM "$SERVER_PID" && wait "$SERVER_PID" 2>/dev/null || true
"$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -db "$WORKDIR/app.db" \
    >>"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS "localhost:$OPS_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done
curl -fsS "$GAME_URL" | grep -q ">X</button>" || fail "game state lost after restart"

step "smoke: graceful shutdown"
kill -TERM "$SERVER_PID"
wait "$SERVER_PID" || fail "server exited non-zero on SIGTERM"
SERVER_PID=""
grep -q "shutting down" "$WORKDIR/server.log" || fail "no graceful shutdown log line"

printf '\nAll gates passed.\n'

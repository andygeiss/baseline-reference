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

step "css: type sizes track the reader's font-size setting"
# clamp()'s middle argument is the fluid one. Built from vw alone it ignores the
# reader's font-size setting and fails WCAG 1.4.4 — and the rem in the min and max
# hides that from a naive grep, so read field 3 specifically. Each clamp is one line.
BAD="$(grep -o 'font-size: *clamp([^)]*)' web/static/css/app.css | awk -F'[(,]' '$3 !~ /rem/')"
[ -z "$BAD" ] || fail "font-size clamp() without a rem term in its fluid middle: $BAD"
BAD="$(grep -o 'font-size: *[0-9.]*px' web/static/css/app.css || true)"
[ -z "$BAD" ] || fail "font-size in px ignores the reader's font-size setting: $BAD"

step "css: icon masks are well-formed data URIs"
# Both failure modes are silent in the stylesheet: an SVG without xmlns is an
# invalid image, which a mask renders fully transparent, and a raw # opens a URL
# fragment that truncates the shape.
BAD="$(grep -- '--icon:' web/static/css/app.css | grep -cv "xmlns='http://www.w3.org/2000/svg'" || true)"
[ "$BAD" = "0" ] || fail "$BAD icon data URI(s) without xmlns — the icon renders transparent"
BAD="$(grep -c -- "--icon:.*#" web/static/css/app.css || true)"
[ "$BAD" = "0" ] || fail "$BAD icon data URI(s) contain a # — it truncates the shape"

step "css: every icon a template asks for is defined"
# A misspelled icon class is silent in both files: the stylesheet keeps its
# shapes and the span renders as a solid 1em square.
for CLASS in $(grep -oh 'icon-[a-z][a-z-]*' web/templates/*.html | sort -u); do
    grep -q -- "\.$CLASS *{" web/static/css/app.css || fail "a template uses .$CLASS, which app.css does not define"
done

step "static build (CGO_ENABLED=0, trimpath)"
CGO_ENABLED=0 go build -trimpath -o "$WORKDIR/server" ./cmd/server

step "config: an invalid value is refused before anything is created"
# The baseline's go-config.md rule: parse and validate first, so a bad value
# costs one line on stderr instead of a half-started process. The database
# assertion is the part with teeth — it fails if validation drifts below the
# call to store.Open.
set +e
"$WORKDIR/server" -port 99999 -database-url "$WORKDIR/never.db" >"$WORKDIR/badcfg.log" 2>&1
CODE=$?
set -e
[ "$CODE" = "2" ] || fail "invalid port: exit $CODE, want 2"
grep -q "want a number from 0 to 65535" "$WORKDIR/badcfg.log" || fail "invalid port: no usable message"
[ ! -e "$WORKDIR/never.db" ] || fail "database created despite invalid config"

step "smoke: test ports are free"
# Without this, a leftover server from an earlier run answers every gate below
# and the gauntlet passes on the wrong binary.
for P in "$PORT" "$OPS_PORT"; do
    if curl -s -o /dev/null --max-time 2 "localhost:$P/"; then
        fail "something is already listening on :$P — stop it, or set PORT/OPS_PORT"
    fi
done

step "smoke test: booting server on :$PORT (ops :$OPS_PORT)"
"$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -database-url "$WORKDIR/app.db" \
    >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS "localhost:$OPS_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done

step "smoke: health endpoint reports ok"
curl -fsS "localhost:$OPS_PORT/healthz" | grep -q '"status":"ok"' || fail "healthz"

step "smoke: home page with CSP header"
curl -fsS -D "$WORKDIR/headers" "localhost:$PORT/" | grep -q "Your list is empty" || fail "home body"
# img-src must keep data:, or the browser blocks every mask icon in app.css.
# base-uri and form-action have no default-src fallback, so dropping either is
# silent: the page keeps working and the protection is simply gone.
grep -qi "content-security-policy: default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'" \
    "$WORKDIR/headers" || fail "CSP header"
grep -qi "strict-transport-security" "$WORKDIR/headers" || fail "HSTS header"
grep -qi "x-content-type-options: nosniff" "$WORKDIR/headers" || fail "nosniff header"
grep -qi "referrer-policy: same-origin" "$WORKDIR/headers" || fail "Referrer-Policy header"

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

step "smoke: plain form flow (add task -> 303 -> list)"
CODE="$(curl -fsS -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/tasks" -d "title=Buy milk")"
[ "$CODE" = "303" ] || fail "plain add: got $CODE, want 303"
curl -fsS "localhost:$PORT/" | grep -q "Buy milk" || fail "the added task is not on the list"

step "smoke: htmx add returns the list fragment, not a page"
FRAGMENT="$(curl -fsS -X POST "localhost:$PORT/tasks" -H 'HX-Request: true' -d "title=Call Ada")"
echo "$FRAGMENT" | grep -q 'id="todo"' || fail "fragment missing the task region"
echo "$FRAGMENT" | grep -q '<html' && fail "fragment is a full page"

step "smoke: an invalid title is refused with 422 and a message"
# 422 is the contract the htmx-config meta tag exists for: htmx 2 does not swap
# 4xx responses without it, so a silent 200 here would look identical in tests
# and break the form in the browser.
CODE="$(curl -s -o "$WORKDIR/invalid.html" -w '%{http_code}' -X POST "localhost:$PORT/tasks" \
    -H 'HX-Request: true' -d "title=%20%20")"
[ "$CODE" = "422" ] || fail "blank title: got $CODE, want 422"
grep -q "Write down what you need to do" "$WORKDIR/invalid.html" || fail "422 without a message"

step "smoke: ticking a task off and deleting it"
# The first row is the oldest open task, "Buy milk". "Call Ada" stays on the
# list, so the restart gate below has something to find.
TASK_ID="$(curl -fsS "localhost:$PORT/" \
    | sed -n 's|.*action="/tasks/\([^/]*\)/toggle".*|\1|p' | head -1)"
[ -n "$TASK_ID" ] || fail "no task id on the list page"
curl -fsS -X POST "localhost:$PORT/tasks/$TASK_ID/toggle" -H 'HX-Request: true' \
    | grep -q 'aria-pressed="true"' || fail "the toggled task is not marked done"
curl -fsS -X POST "localhost:$PORT/tasks/$TASK_ID/delete" -H 'HX-Request: true' >/dev/null
curl -fsS "localhost:$PORT/" | grep -q "Buy milk" && fail "the deleted task is still on the list"

step "smoke: cross-site POST rejected (CSRF)"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/tasks" \
    -H 'Sec-Fetch-Site: cross-site' -d "title=Nope")"
[ "$CODE" = "403" ] || fail "CSRF: got $CODE, want 403"

step "smoke: state survives restart"
kill -TERM "$SERVER_PID" && wait "$SERVER_PID" 2>/dev/null || true
"$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -database-url "$WORKDIR/app.db" \
    >>"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS "localhost:$OPS_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done
curl -fsS "localhost:$PORT/" | grep -q "Call Ada" || fail "the list was lost after the restart"

step "smoke: graceful shutdown"
kill -TERM "$SERVER_PID"
wait "$SERVER_PID" || fail "server exited non-zero on SIGTERM"
SERVER_PID=""
grep -q "shutting down" "$WORKDIR/server.log" || fail "no graceful shutdown log line"

printf '\nAll gates passed.\n'

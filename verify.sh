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
JAR="$WORKDIR/cookies"
INVITE="open-sesame-1234"
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

step "css: a list that drops its markers keeps its semantics"
# css-layout.md: Safari hands a marker-less list to VoiceOver as plain content,
# so "list, N items" is never announced. role="list" is the one sanctioned ARIA
# patch — and nothing in either file shows when it is missing.
for CLASS in $(awk '
    /^[[:space:]]*\.[a-z][a-z-]*[[:space:]]*\{/ { sel = $1; sub(/^\./, "", sel) }
    /list-style:[[:space:]]*none/ && sel != ""   { print sel }
' web/static/css/app.css | sort -u); do
    if grep -hE "class=\"[^\"]*\<$CLASS\>" web/templates/*.html | grep -qv 'role="list"'; then
        fail ".$CLASS drops its markers, but a template writes it without role=\"list\""
    fi
done

step "the adapter depends on the domain and nothing else of ours"
# patterns/go-ports-adapters.md: internal/chatapi is the only package that knows
# the server speaks HTTP. If it ever imports internal/app, the dependency
# direction has been inverted and the CLI drags the whole web application in.
DEPS="$(go list -deps ./internal/chatapi | grep baseline-reference | grep -v 'internal/chatapi$' || true)"
[ "$DEPS" = "github.com/andygeiss/baseline-reference/v3/internal/domain" ] \
    || fail "internal/chatapi depends on more than the domain: $DEPS"

step "static build (CGO_ENABLED=0, trimpath)"
CGO_ENABLED=0 go build -trimpath -o "$WORKDIR/server" ./cmd/server
CGO_ENABLED=0 go build -trimpath -o "$WORKDIR/gochat" ./cmd/gochat

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

step "config: -h names the environment and the credentials, not only the flags"
# go-config.md rule 5: -h is the whole contract, so a variable that appears in
# no flag's help text still has to be listed.
set +e
"$WORKDIR/server" -h >"$WORKDIR/help.txt" 2>&1
set -e
grep -q "ENV" "$WORKDIR/help.txt" || fail "-h does not name ENV"
grep -q "CREDENTIALS_DIRECTORY" "$WORKDIR/help.txt" || fail "-h does not name the credentials directory"
grep -q "invite_code" "$WORKDIR/help.txt" || fail "-h does not name the invite_code credential"

step "smoke: test ports are free"
# Without this, a leftover server from an earlier run answers every gate below
# and the gauntlet passes on the wrong binary.
for P in "$PORT" "$OPS_PORT"; do
    if curl -s -o /dev/null --max-time 2 "localhost:$P/"; then
        fail "something is already listening on :$P — stop it, or set PORT/OPS_PORT"
    fi
done

step "smoke: booting server on :$PORT (ops :$OPS_PORT)"
# The secret arrives as a file in a directory the deployment names — never as a
# flag, which shows up in ps, and never as an environment variable, which every
# child process inherits (patterns/go-config.md).
mkdir -p "$WORKDIR/creds"
printf '%s\n' "$INVITE" > "$WORKDIR/creds/invite_code"
CREDENTIALS_DIRECTORY="$WORKDIR/creds" \
    "$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -database-url "$WORKDIR/app.db" \
    >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS "localhost:$OPS_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done

step "smoke: health endpoint reports ok"
curl -fsS "localhost:$OPS_PORT/healthz" | grep -q '"status":"ok"' || fail "healthz"

step "smoke: sign-in page with CSP header"
curl -fsS -D "$WORKDIR/headers" "localhost:$PORT/login" | grep -q "Sign in" || fail "login body"
# img-src must keep data:, or the browser blocks every mask icon in app.css.
# base-uri and form-action have no default-src fallback, so dropping either is
# silent: the page keeps working and the protection is simply gone.
grep -qi "content-security-policy: default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'" \
    "$WORKDIR/headers" || fail "CSP header"
grep -qi "strict-transport-security" "$WORKDIR/headers" || fail "HSTS header"
grep -qi "x-content-type-options: nosniff" "$WORKDIR/headers" || fail "nosniff header"
grep -qi "referrer-policy: same-origin" "$WORKDIR/headers" || fail "Referrer-Policy header"

step "smoke: install manifest linked, served as application/manifest+json"
curl -fsS "localhost:$PORT/login" | grep -q 'rel="manifest"' || fail "manifest not linked from layout"
curl -fsS -D "$WORKDIR/mheaders" "localhost:$PORT/static/manifest.webmanifest" \
    | grep -q '"start_url": "/"' || fail "manifest body"
# Go's built-in mime table has no .webmanifest entry and the Unix mime files it
# merges vary by host: this gate proves main.go's AddExtensionType is doing it.
grep -qi "content-type: application/manifest+json" "$WORKDIR/mheaders" || fail "manifest content-type"

step "smoke: install icons served"
for ICON in icon-192.png icon-512.png icon-512-maskable.png apple-touch-icon.png; do
    curl -fsS -o /dev/null "localhost:$PORT/static/$ICON" || fail "icon missing: $ICON"
done

step "smoke: static directory URLs are 404, not a browsable index"
CODE="$(curl -s -o /dev/null -w '%{http_code}' "localhost:$PORT/static/css/")"
[ "$CODE" = "404" ] || fail "a static directory URL answered $CODE, want 404"

step "smoke: signed out, every private route sends you to the sign-in page"
for PATH_ in / /rooms /rooms/general /profile; do
    CODE="$(curl -s -o /dev/null -w '%{http_code}' "localhost:$PORT$PATH_")"
    [ "$CODE" = "303" ] || fail "GET $PATH_ signed out: got $CODE, want 303"
done

step "smoke: registration needs the invite code from the credential file"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/register" \
    -d "name=Mallory&password=correct-horse&invite=guess")"
[ "$CODE" = "422" ] || fail "a wrong invite code: got $CODE, want 422"

step "smoke: the invite code never reaches the logs"
# The Config allowlist is what keeps it out: LogValue names every safe field and
# omits this one, so adding a secret to the struct does not add it to the log.
grep -q "$INVITE" "$WORKDIR/server.log" && fail "the invite code was written to the log"
grep -q "registration_gated=true" "$WORKDIR/server.log" || fail "the boot log does not record that registration is gated"

step "smoke: registering with the right code signs you in"
CODE="$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/register" \
    -d "name=Ada&password=correct-horse&invite=$INVITE")"
[ "$CODE" = "303" ] || fail "registering: got $CODE, want 303"

step "smoke: the session cookie carries its flags"
# scs defaults HttpOnly to true and SameSite to Lax, but Secure to false. This
# binary speaks plain HTTP behind a TLS proxy, so Secure follows ENV — it is off
# here, and a gate that demanded it would be demanding a cookie no client sends.
grep -q "session" "$JAR" || fail "no session cookie was set"
curl -s -D "$WORKDIR/cookieheaders" -c "$JAR" -b "$JAR" -o /dev/null "localhost:$PORT/rooms"
FLAGS="$(grep -i '^set-cookie: session' "$WORKDIR/cookieheaders" || true)"
if [ -n "$FLAGS" ]; then
    echo "$FLAGS" | grep -qi "HttpOnly" || fail "the session cookie is not HttpOnly"
    echo "$FLAGS" | grep -qi "SameSite=Lax" || fail "the session cookie is not SameSite=Lax"
fi

step "smoke: signing in again renews the session token"
# Without RenewToken a token an attacker planted before the login still works
# after it.
BEFORE="$(awk '/session/ {print $7}' "$JAR" | tail -1)"
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST "localhost:$PORT/logout"
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST "localhost:$PORT/login" \
    -d "name=Ada&password=correct-horse"
AFTER="$(awk '/session/ {print $7}' "$JAR" | tail -1)"
[ -n "$BEFORE" ] && [ -n "$AFTER" ] || fail "no session token to compare"
[ "$BEFORE" != "$AFTER" ] || fail "the session token survived the login"

step "smoke: a wrong password and an unknown name answer the same way"
curl -s -o "$WORKDIR/wrongpw.html" -X POST "localhost:$PORT/login" \
    -d "name=Ada&password=wrong-horse"
curl -s -o "$WORKDIR/nouser.html" -X POST "localhost:$PORT/login" \
    -d "name=Nobody&password=correct-horse"
for F in "$WORKDIR/wrongpw.html" "$WORKDIR/nouser.html"; do
    grep -q "We do not know that name and password." "$F" \
        || fail "$(basename "$F") does not carry the shared message"
done

step "smoke: repeated sign-in attempts are rate limited"
# The bucket holds five, then refills one every three seconds. Nothing else is
# limited — the polling route is hit by every open tab on a timer.
LIMITED=""
for _ in 1 2 3 4 5 6 7 8; do
    CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/login" \
        -H 'X-Forwarded-For: 203.0.113.9' -d "name=Ada&password=wrong-horse")"
    [ "$CODE" = "429" ] && LIMITED="yes" && break
done
[ -n "$LIMITED" ] || fail "eight bad sign-in attempts were never rate limited"
curl -s -D "$WORKDIR/limited" -o /dev/null -X POST "localhost:$PORT/login" \
    -H 'X-Forwarded-For: 203.0.113.9' -d "name=Ada&password=wrong-horse"
grep -qi "retry-after" "$WORKDIR/limited" || fail "a 429 without Retry-After says nothing about when to return"

step "smoke: creating a room, and the dialog that offers it"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/rooms.html" "localhost:$PORT/rooms"
grep -q 'command="show-modal"' "$WORKDIR/rooms.html" || fail "no invoker button opens the new-room dialog"
grep -q '<dialog id="new-room"' "$WORKDIR/rooms.html" || fail "no dialog to open"
# stack/html.md asks for a full-page fallback beside the dialog, for a browser
# that does not know invoker commands.
curl -fsS -c "$JAR" -b "$JAR" -o /dev/null "localhost:$PORT/rooms/new" || fail "the dialog has no full-page fallback"
CODE="$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/rooms" -d "name=General Chat")"
[ "$CODE" = "303" ] || fail "creating a room: got $CODE, want 303"

step "smoke: the bottom bar marks where you are"
# Colour alone would not reach a screen reader (patterns/css-layout.md).
grep -q 'href="/rooms" aria-current="page"' "$WORKDIR/rooms.html" || fail "the bar does not mark the current section"
curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/profile" | grep -q 'href="/profile" aria-current="page"' \
    || fail "the profile page does not mark its own section"

step "smoke: a room slugging to a route is refused"
CODE="$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/rooms" -d "name=New")"
[ "$CODE" = "422" ] || fail "a room named New: got $CODE, want 422 (it would shadow /rooms/new)"

step "smoke: plain form flow (post a message -> 303 -> the room)"
CODE="$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' \
    -X POST "localhost:$PORT/rooms/general-chat/messages" -d "body=hello from a form")"
[ "$CODE" = "303" ] || fail "plain post: got $CODE, want 303"
curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/rooms/general-chat" | grep -q "hello from a form" \
    || fail "the posted message is not in the room"

step "smoke: htmx post returns the chat region, not a page"
FRAGMENT="$(curl -fsS -c "$JAR" -b "$JAR" -X POST "localhost:$PORT/rooms/general-chat/messages" \
    -H 'HX-Request: true' -d "body=hello from htmx")"
echo "$FRAGMENT" | grep -q 'id="chat"' || fail "fragment missing the chat region"
echo "$FRAGMENT" | grep -q '<html' && fail "fragment is a full page"

step "smoke: the poll answers 204 when there is nothing new"
# htmx does not swap a 204, so the poller keeps the cursor it has. This is the
# quiet case, and it is most of them.
LATEST="$(curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/rooms/general-chat" \
    | sed -n 's|.*messages?since=\([0-9]*\).*|\1|p' | head -1)"
[ -n "$LATEST" ] || fail "the room page carries no cursor"
CODE="$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' -H 'HX-Request: true' \
    "localhost:$PORT/rooms/general-chat/messages?since=$LATEST")"
[ "$CODE" = "204" ] || fail "a current cursor: got $CODE, want 204"

step "smoke: the poll brings back what is new, and moves the cursor on"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/poll.html" -H 'HX-Request: true' \
    "localhost:$PORT/rooms/general-chat/messages?since=0"
grep -q "hello from a form" "$WORKDIR/poll.html" || fail "the poll missed a message"
grep -q 'id="poll"' "$WORKDIR/poll.html" || fail "the poll answer carries no new poller, so polling would stop"
grep -q "since=$LATEST" "$WORKDIR/poll.html" || fail "the poll did not move the cursor on"

step "smoke: the polling route is htmx-only"
# It is an optimization of "reload the page", not a feature of its own, so a
# plain reader is sent to the page rather than handed a fragment.
CODE="$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' \
    "localhost:$PORT/rooms/general-chat/messages?since=0")"
[ "$CODE" = "303" ] || fail "the poll route without htmx: got $CODE, want 303"

step "smoke: an empty message is refused with 422 and a message"
# 422 is the contract the htmx-config meta tag exists for: htmx 2 does not swap
# 4xx responses without it, so a silent 200 here would look identical in tests
# and break the form in the browser.
CODE="$(curl -s -c "$JAR" -b "$JAR" -o "$WORKDIR/invalid.html" -w '%{http_code}' \
    -X POST "localhost:$PORT/rooms/general-chat/messages" -H 'HX-Request: true' -d "body=%20%20")"
[ "$CODE" = "422" ] || fail "an empty message: got $CODE, want 422"
grep -q "Write something first" "$WORKDIR/invalid.html" || fail "422 without a message"
# go-forms-validation.md: adjacent is enough for the eye and nothing for a
# screen reader, so the failing control points at the message it just grew.
grep -q 'aria-invalid="true"' "$WORKDIR/invalid.html" || fail "422 without aria-invalid on the field"
grep -q 'aria-describedby="body-error"' "$WORKDIR/invalid.html" || fail "422 without aria-describedby on the field"
grep -q 'id="body-error"' "$WORKDIR/invalid.html" || fail "aria-describedby points at no element"
# Both appear only when the error does — a permanently invalid field says nothing.
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/valid.html" "localhost:$PORT/rooms/general-chat"
grep -q 'aria-invalid' "$WORKDIR/valid.html" && fail "aria-invalid on a form with no error"
grep -q 'role="list"' "$WORKDIR/valid.html" || fail "the rendered list lost its role=\"list\""

step "smoke: a message is text, never markup"
# The whole product is user-written text on a shared page, so this is the gate
# that matters most.
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST "localhost:$PORT/rooms/general-chat/messages" \
    --data-urlencode 'body=<script>alert("xss")</script>'
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/escaped.html" "localhost:$PORT/rooms/general-chat"
grep -q '<script>alert' "$WORKDIR/escaped.html" && fail "a message reached the page as live markup"
grep -q '&lt;script&gt;' "$WORKDIR/escaped.html" || fail "the message was not escaped into text"

step "smoke: a machine token signs a program in"
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST "localhost:$PORT/profile/tokens" -d "label=verify.sh"
SECRET="$(curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/profile" | grep -o 'gochat_[A-Za-z0-9_-]*' | head -1)"
[ -n "$SECRET" ] || fail "the profile page showed no token secret"
# Shown once: the server kept only the hash, so there is nothing left to show.
curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/profile" | grep -q "$SECRET" \
    && fail "the token secret came back on a second visit"
# No cookie at all: the token is the only credential.
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SECRET" "localhost:$PORT/rooms")"
[ "$CODE" = "200" ] || fail "a machine token: got $CODE, want 200"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer gochat_nothing" "localhost:$PORT/rooms")"
[ "$CODE" = "401" ] || fail "a bad token: got $CODE, want 401 (a program cannot read a login page)"

step "smoke: the JSON surface needs a token, and says so in JSON"
# /api is its own surface: a program cannot read a sign-in page, and a 303 to
# one would look like success to anything checking only the status.
curl -s -o "$WORKDIR/api401.json" -D "$WORKDIR/api401.head" -w '%{http_code}' \
    "localhost:$PORT/api/rooms" > "$WORKDIR/api401.code"
[ "$(cat "$WORKDIR/api401.code")" = "401" ] || fail "GET /api/rooms with no token: want 401"
grep -qi "content-type: application/json" "$WORKDIR/api401.head" || fail "/api answered in something other than JSON"
grep -q '"error"' "$WORKDIR/api401.json" || fail "a 401 from /api with no reason in it"

step "smoke: the command-line client talks to the server"
printf '%s\n' "$SECRET" > "$WORKDIR/token"
export GOCHAT_ADDR="http://localhost:$PORT"
export GOCHAT_TOKEN_FILE="$WORKDIR/token"
[ "$("$WORKDIR/gochat" whoami)" = "Ada" ] || fail "gochat whoami did not name the token's owner"
"$WORKDIR/gochat" rooms | grep -q "^general-chat	General Chat$" || fail "gochat rooms did not list the room"
SEQ="$("$WORKDIR/gochat" post general-chat "hello from the command line")"
[ -n "$SEQ" ] || fail "gochat post printed no cursor for a script to carry on from"
"$WORKDIR/gochat" read -json general-chat | grep -q '"body":"hello from the command line"' \
    || fail "gochat read -json did not bring the message back"
# stdout is data and stderr is everything else, so the cursor must not be in
# the pipe: `gochat read | wc -l` counts messages, never notes.
"$WORKDIR/gochat" read general-chat 2>/dev/null | grep -q "next cursor" \
    && fail "the cursor was written to stdout, where the data goes"
# The message the browser posted and the one the CLI posted are the same room.
curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/rooms/general-chat" \
    | grep -q "hello from the command line" || fail "the CLI's message is not on the page"

step "smoke: the client reads a message from a pipe"
echo "piped in" | "$WORKDIR/gochat" post general-chat - >/dev/null || fail "gochat post could not read stdin"
"$WORKDIR/gochat" read -json general-chat | grep -q '"body":"piped in"' || fail "the piped message did not arrive"

step "smoke: the client's exit codes"
set +e
"$WORKDIR/gochat" dance >/dev/null 2>&1; CODE=$?
set -e
[ "$CODE" = "2" ] || fail "an unknown command exited $CODE, want 2"
set +e
GOCHAT_TOKEN_FILE="" CHAT_TOKEN="gochat_nope" "$WORKDIR/gochat" rooms >/dev/null 2>&1; CODE=$?
set -e
[ "$CODE" = "1" ] || fail "a refused token exited $CODE, want 1"
unset GOCHAT_ADDR GOCHAT_TOKEN_FILE

step "smoke: revoking a token stops it"
TOKEN_ID="$(curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/profile" \
    | sed -n 's|.*action="/profile/tokens/\([^/]*\)/delete".*|\1|p' | head -1)"
[ -n "$TOKEN_ID" ] || fail "no token id on the profile page"
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST "localhost:$PORT/profile/tokens/$TOKEN_ID/delete"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SECRET" "localhost:$PORT/rooms")"
[ "$CODE" = "401" ] || fail "a revoked token still answers $CODE — revocation is a DELETE, not a flag"

step "smoke: cross-site POST rejected (CSRF)"
CODE="$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/rooms" \
    -H 'Sec-Fetch-Site: cross-site' -d "name=Nope")"
[ "$CODE" = "403" ] || fail "CSRF: got $CODE, want 403"

step "smoke: the backup snapshot is written and opens"
# patterns/go-sqlite.md: this app's answer to "if the server disappears right
# now, what have you lost?" is "up to a day", so it writes a consistent copy
# beside the database at boot and once a day after.
SNAPSHOT="$(ls "$WORKDIR"/snapshot-*.db 2>/dev/null | head -1)"
[ -n "$SNAPSHOT" ] || fail "no snapshot was written at boot"
# A snapshot that cannot be read is not a backup. VACUUM INTO writes a whole
# database, so sqlite's own header is what proves it.
head -c 15 "$SNAPSHOT" | grep -q "SQLite format 3" || fail "the snapshot is not a SQLite database"

step "smoke: state survives restart"
kill -TERM "$SERVER_PID" && wait "$SERVER_PID" 2>/dev/null || true
CREDENTIALS_DIRECTORY="$WORKDIR/creds" \
    "$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -database-url "$WORKDIR/app.db" \
    >>"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS "localhost:$OPS_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done
# The session outlives the restart too: it lives in SQLite, not in memory.
curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/rooms/general-chat" | grep -q "hello from a form" \
    || fail "the conversation was lost after the restart"

step "smoke: graceful shutdown"
kill -TERM "$SERVER_PID"
wait "$SERVER_PID" || fail "server exited non-zero on SIGTERM"
SERVER_PID=""
grep -q "shutting down" "$WORKDIR/server.log" || fail "no graceful shutdown log line"

printf '\nAll gates passed.\n'

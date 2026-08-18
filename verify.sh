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

step "the model adapter depends on the domain and nothing else of ours"
# patterns/go-llm-adapter.md: internal/anthropic is the only package that knows
# Anthropic's API. If it ever imports internal/app, the port has been inverted
# and the vendor's shape has reached the application.
DEPS="$(go list -deps ./internal/anthropic | grep baseline-reference | grep -v 'internal/anthropic$' || true)"
[ "$DEPS" = "github.com/andygeiss/baseline-reference/v3/internal/domain" ] \
    || fail "internal/anthropic depends on more than the domain: $DEPS"

step "the prompt lives in the domain, not in an adapter"
# One prompt, shared. An adapter carrying its own copy is two products that
# answer differently depending on a flag nobody connects to the symptom.
grep -q "SystemPrompt" internal/domain/assistant.go || fail "the system prompt is not in the domain"
for ADAPTER in internal/anthropic/anthropic.go internal/echo/echo.go; do
    grep -q "You are a participant" "$ADAPTER" && fail "$ADAPTER carries its own copy of the prompt"
done

step "every column naming a user says what happens when that user goes"
# patterns/go-data-deletion.md rule 2. A user column with no REFERENCES clause
# cascades from nothing: the delete succeeds, the rows stay, and every test is
# still green. The defect is an absence, so this gate looks for the absence
# rather than for a symptom.
MISSING="$(grep -hE '^[[:space:]]*(user_id|author_id|uploader_id)[[:space:]]+TEXT|ADD COLUMN[[:space:]]+(user_id|author_id|uploader_id)[[:space:]]+TEXT' \
    internal/store/migrations/*.sql | grep -v 'ON DELETE' || true)"
[ -z "$MISSING" ] || fail "a column names a user and says nothing about their deletion:$MISSING"

step "every child of users is indexed"
# Deleting a parent runs SELECT rowid FROM <child> WHERE <child key> = ? once
# per child table. Without an index that is a linear scan, once per table.
#
# Table and column together, not the column name alone: two tables share the
# name user_id, and indexing one of them would otherwise answer for both.
for PAIR in $(awk '
    /^CREATE TABLE/ { t = $3; sub(/\(.*/, "", t) }
    /^ALTER TABLE/  { t = $3 }
    /(user_id|author_id|uploader_id)[ \t]+TEXT/ {
        for (i = 1; i <= NF; i++)
            if ($i ~ /^(user_id|author_id|uploader_id)$/) print t "." $i
    }
' internal/store/migrations/*.sql | sort -u); do
    TBL="${PAIR%.*}"
    COL="${PAIR#*.}"
    grep -qE "CREATE (UNIQUE )?INDEX[[:space:]]+[a-z_]+[[:space:]]+ON[[:space:]]+$TBL[[:space:]]*\\($COL\\)" \
        internal/store/migrations/*.sql \
        || fail "$TBL.$COL is a child of users with no index: every account deletion scans $TBL"
done

step "the account delete is confirmed by the server, not by a dialog"
# patterns/go-data-deletion.md: hx-confirm is what the token revoke uses, and it
# is drawn by htmx -- so it is nothing at all with htmx switched off, and the
# server cannot check it. An irreversible action asks for something the server
# verifies: here the name retyped, and the password.
grep -q 'hx-confirm' web/templates/account-delete.html \
    && fail "the delete page leans on hx-confirm, which the server cannot check"
grep -q 'name="password"' web/templates/account-delete.html \
    || fail "the delete page does not ask for the password"
grep -q 'name="name"' web/templates/account-delete.html \
    || fail "the delete page does not ask for the account name to be retyped"

step "local HTTPS: the binary never serves TLS"
# patterns/local-https.md rule 1. The tempting fix for "install needs HTTPS" is a
# -tls-cert flag. It splits the server into two shapes, and the one nobody deploys
# is the one that gets tested. Caddy in front is the answer here and in production.
# Serving TLS is what is banned, not using it: an outbound client that pins its own
# roots is fine, so this looks for the serving calls rather than for crypto/tls.
BAD="$(grep -rln 'ListenAndServeTLS\|ServeTLS\|tls-cert\|tls-key' --include='*.go' . || true)"
[ -z "$BAD" ] || fail "the binary serves TLS: $BAD"

step "local HTTPS: Caddyfile.lan is the local artefact, not the deployment one"
# patterns/local-https.md rule 2. An edited copy of the deployment Caddyfile drifts
# from a template nothing rebuilds it from. Three markers make this the local file:
# its own authority, no :80 redirect listener, and a loopback upstream.
[ -f Caddyfile.lan ] || fail "Caddyfile.lan is missing, but the README opts this project in"
grep -q 'tls internal' Caddyfile.lan || fail "Caddyfile.lan does not use the local authority (tls internal)"
grep -q 'auto_https disable_redirects' Caddyfile.lan || fail "Caddyfile.lan would bind :80 to redirect to a port it does not serve"
grep -q 'reverse_proxy 127.0.0.1:8080' Caddyfile.lan || fail "Caddyfile.lan does not proxy to the app on loopback"

step "local HTTPS: nothing that ships reads Caddyfile.lan"
# patterns/local-https.md rule 3. A deployment that needs this file is a deployment
# doing something wrong, and an embedded copy would ship a developer's hostname.
BAD="$(grep -rln 'Caddyfile.lan' --include='*.go' . || true)"
[ -z "$BAD" ] || fail "Go code names Caddyfile.lan: $BAD"

step "local HTTPS: no certificate or key was ever committed"
# patterns/local-https.md rule 4. The authority stays on the machine that made it.
# A committed root key mints certificates every trusting device accepts.
BAD="$(git log --all --pretty=format: --name-only | sort -u | grep -E '\.(crt|key|pem|p12)$' || true)"
[ -z "$BAD" ] || fail "a certificate or key is in this repository's history: $BAD"

step "local HTTPS: the README says the project opts in"
# patterns/local-https.md rule 5. The feature is invisible until someone follows
# the steps, so the opt-in is written next to how to run the app.
grep -q 'opts into local HTTPS' README.md || fail "the README does not record the opt-in"

step "local HTTPS: the Caddyfile is valid"
# Read by Caddy rather than by eye. Caddy is a system binary, not a `go run` tool,
# so it is absent on plenty of machines that still need a green run — skipped there,
# and the five gates above still hold.
if command -v caddy >/dev/null 2>&1; then
    caddy validate --config Caddyfile.lan >/dev/null 2>&1 || fail "caddy validate rejects Caddyfile.lan"
else
    echo "   caddy is not installed here — the config is validated where it is"
fi

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

step "config: two settings that are one setting are checked as a pair"
# go-config.md rule 7: -assistant=anthropic and its credential are one setting
# in two halves. Rule 2 checks one field at a time and cannot see a pair, so
# half-configured the app would start fine and fail on the first mention —
# the worst place to find out.
set +e
"$WORKDIR/server" -assistant anthropic -database-url "$WORKDIR/never2.db" >"$WORKDIR/pair.log" 2>&1
CODE=$?
set -e
[ "$CODE" = "2" ] || fail "assistant without a key: exit $CODE, want 2"
# STYLE.md: name the fix, not just the fault — both ways out are in the line.
grep -q "anthropic_key" "$WORKDIR/pair.log" || fail "the error does not name the file to write"
grep -q "assistant=echo" "$WORKDIR/pair.log" || fail "the error does not name the other way out"
[ ! -e "$WORKDIR/never2.db" ] || fail "database created despite a half-configured pair"

# The other half of the pair: a key with no adapter to use it. Its own
# directory — the boot below needs a credentials directory holding the invite
# code and nothing else, and this rule is strict on purpose.
mkdir -p "$WORKDIR/creds-pair"
printf 'sk-test\n' > "$WORKDIR/creds-pair/anthropic_key"
set +e
CREDENTIALS_DIRECTORY="$WORKDIR/creds-pair" "$WORKDIR/server" -assistant echo \
    -database-url "$WORKDIR/never3.db" >"$WORKDIR/pair2.log" 2>&1
CODE=$?
set -e
[ "$CODE" = "2" ] || fail "a key with -assistant=echo: exit $CODE, want 2"
grep -q "assistant=anthropic" "$WORKDIR/pair2.log" || fail "the error does not name the fix"

step "config: an unknown time zone is refused at boot, not at the first page"
# time.LoadLocation runs once, in parseConfig. A name nobody has heard of is a
# line on stderr, not a nil location found weeks later by the one reader who
# looked at a timestamp (patterns/time-and-dates.md).
set +e
"$WORKDIR/server" -timezone Mars/Olympus -database-url "$WORKDIR/never-tz.db" \
    >"$WORKDIR/tz.log" 2>&1
CODE=$?
set -e
[ "$CODE" = "2" ] || fail "an unknown zone: exit $CODE, want 2"
grep -q "Europe/Berlin" "$WORKDIR/tz.log" || fail "the error does not show what a zone name looks like"
[ ! -e "$WORKDIR/never-tz.db" ] || fail "database created despite an unknown zone"

step "config: how mail goes out is checked as one setting, not four"
# Each of these starts fine and fails at the one moment somebody needed it — a
# person who cannot sign in asking for a link (patterns/go-config.md rule 7).
set +e
"$WORKDIR/server" -mailer smtp -database-url "$WORKDIR/never-mail.db" \
    >"$WORKDIR/mail1.log" 2>&1
CODE=$?
set -e
[ "$CODE" = "2" ] || fail "smtp with no relay: exit $CODE, want 2"
grep -q "smtp-addr" "$WORKDIR/mail1.log" || fail "the error does not name the flag to set"
grep -q "mailer=log" "$WORKDIR/mail1.log" || fail "the error does not name the other way out"

set +e
ENV=prod "$WORKDIR/server" -mailer log -database-url "$WORKDIR/never-mail2.db" \
    >"$WORKDIR/mail2.log" 2>&1
CODE=$?
set -e
# logmail writes the whole message, reset link and all, to the log. That is what
# makes it useful on a laptop and unacceptable where the log is not one person's.
[ "$CODE" = "2" ] || fail "the logging mailer in production: exit $CODE, want 2"
grep -q "mailer=smtp" "$WORKDIR/mail2.log" || fail "the error does not name the fix"
[ ! -e "$WORKDIR/never-mail.db" ] || fail "database created despite a half-configured mailer"

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
# -timezone is a named zone on purpose: a minimal container has no
# /usr/share/zoneinfo, so booting with one is what proves the database is inside
# the binary (patterns/time-and-dates.md).
CREDENTIALS_DIRECTORY="$WORKDIR/creds" \
    "$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -database-url "$WORKDIR/app.db" \
    -timezone Europe/Berlin \
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
# binary speaks plain HTTP behind a TLS proxy, so Secure follows ENV and is off in
# this dev run. Only the two unconditional flags are gated: demanding Secure here
# would pin a production-only setting, and this run over loopback cannot tell the
# difference anyway — curl returns a Secure cookie to localhost either way.
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
# A picture on its own is a message, so the words name both ways to send one.
grep -q "Write something, or attach a file" "$WORKDIR/invalid.html" || fail "422 without a message"
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

step "smoke: an upload is stored as what its bytes are, not what it claims"
# A document that can carry script, named .png and declared image/png. Neither
# the name nor the declaration is consulted (patterns/go-file-uploads.md).
printf '<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>' > "$WORKDIR/avatar.png"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/lied.html" -H 'HX-Request: true' \
    -F 'body=totally a picture' \
    -F "file=@$WORKDIR/avatar.png;type=image/png;filename=avatar.png" \
    "localhost:$PORT/rooms/general-chat/messages"
FILE_URL="$(sed -n 's|.*href="\(/rooms/general-chat/files/[^"/]*\)".*|\1|p' "$WORKDIR/lied.html" | head -1)"
[ -n "$FILE_URL" ] || fail "the uploaded file is not linked from the room"
curl -fsS -c "$JAR" -b "$JAR" -D "$WORKDIR/file.head" -o /dev/null "localhost:$PORT$FILE_URL"
grep -qi '^content-type: *image/' "$WORKDIR/file.head" && fail "an SVG came back as an image"
grep -qi '^content-type:.*\(svg\|html\)' "$WORKDIR/file.head" && fail "an SVG came back as a document"
# Anything the app does not render in its own pages downloads instead of opening.
grep -qi '^content-disposition: *attachment' "$WORKDIR/file.head" \
    || fail "a file the app does not render inline is not sent as an attachment"
# nosniff is what makes the stored type binding rather than a hint.
grep -qi '^x-content-type-options: *nosniff' "$WORKDIR/file.head" \
    || fail "the download is served without nosniff"

step "smoke: a picture round-trips byte for byte"
# The usual way to lose the first 512 bytes is to sniff them off a stream and
# store what is left, so the whole file is compared.
printf '\211PNG\r\n\032\n' > "$WORKDIR/real.png"
head -c 64 /dev/urandom >> "$WORKDIR/real.png"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/png.html" -H 'HX-Request: true' \
    -F 'body=' -F "file=@$WORKDIR/real.png;type=application/octet-stream;filename=shot.png" \
    "localhost:$PORT/rooms/general-chat/messages"
PNG_URL="$(sed -n 's|.*<img src="\([^"]*\)".*|\1|p' "$WORKDIR/png.html" | head -1)"
[ -n "$PNG_URL" ] || fail "the picture is not rendered in the room"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/back.png" "localhost:$PORT$PNG_URL"
cmp -s "$WORKDIR/real.png" "$WORKDIR/back.png" || fail "the picture did not survive the round trip"

step "smoke: a long room pages backwards, and the two cursors stay apart"
i=1
while [ "$i" -le 55 ]; do
    curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST \
        "localhost:$PORT/rooms/general-chat/messages" --data-urlencode "body=line $i"
    i=$((i + 1))
done
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/long.html" "localhost:$PORT/rooms/general-chat"
grep -q 'older?before=' "$WORKDIR/long.html" || fail "a long room offers no way back through it"
# One cursor walks forward at the arrival end, one backward at the far end.
# Sharing a name is how they answer each other's questions (patterns/htmx-lists.md).
grep -q 'messages?since=' "$WORKDIR/long.html" || fail "the poller's cursor is gone"
grep -q 'older?since=' "$WORKDIR/long.html" && fail "the two cursors have swapped names"
grep -q '>line 1<' "$WORKDIR/long.html" && fail "the whole room came back at once — nothing is paged"
BEFORE="$(sed -n 's|.*older?before=\([0-9]*\).*|\1|p' "$WORKDIR/long.html" | head -1)"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/older.html" -H 'HX-Request: true' \
    "localhost:$PORT/rooms/general-chat/older?before=$BEFORE"
grep -q '<html' "$WORKDIR/older.html" && fail "the older fragment is a whole page"
grep -q '>line 1<' "$WORKDIR/older.html" || fail "paging back did not reach the older messages"
# The same URL without htmx is a whole page, which is what makes it linkable.
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/older-page.html" \
    "localhost:$PORT/rooms/general-chat/older?before=$BEFORE"
grep -q '<!doctype html>' "$WORKDIR/older-page.html" || fail "a plain click on Show older got a fragment"

step "smoke: every time on a page is machine-readable and names its zone"
grep -q '<time datetime="' "$WORKDIR/long.html" || fail "the room carries no machine-readable time"
# "+0000 UTC" is what {{.CreatedAt}} prints. Finding it means a time.Time
# reached a template (patterns/time-and-dates.md).
grep -q '+0000 UTC' "$WORKDIR/long.html" && fail "a raw time.Time reached a template"
# The zone the deployment named, with its abbreviation, because "11:14" is a
# different moment for every reader. This also proves tzdata is in the binary.
grep -qE 'CES?T' "$WORKDIR/long.html" || fail "the rendered time does not name the configured zone"

step "smoke: the assistant answers a mention, and stays out of the way otherwise"
# The whole loop with no key and no model: -assistant=echo is the default, so
# an empty environment exercises mention -> port -> adapter -> storage -> the
# page (patterns/go-llm-adapter.md rule 14).
curl -fsS -c "$JAR" -b "$JAR" -o /dev/null -X POST \
    "localhost:$PORT/rooms/general-chat/messages" -d "body=quiet+line+nobody+asked+about"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/quiet.html" "localhost:$PORT/rooms/general-chat"
grep -q "echo:" "$WORKDIR/quiet.html" && fail "the assistant answered a message that did not mention it"

curl -fsS -c "$JAR" -b "$JAR" -o /dev/null -X POST \
    "localhost:$PORT/rooms/general-chat/messages" -d "body=%40assistant+are+you+there%3F"
curl -fsS -c "$JAR" -b "$JAR" -o "$WORKDIR/mention.html" "localhost:$PORT/rooms/general-chat"
grep -q "echo:" "$WORKDIR/mention.html" || fail "the assistant did not answer a mention"
# It posts as a user like anybody else, so the join that reads a room resolves
# its name — migration 0002 seeds that row.
grep -q "Assistant" "$WORKDIR/mention.html" || fail "the reply is not attributed to the assistant"
# The required step stands on its own: the person's message is in the room
# whatever the model did.
grep -q "are you there" "$WORKDIR/mention.html" || fail "the mention itself is missing from the room"

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

step "smoke: another signed-in user cannot revoke your token"
# patterns/go-authorization.md: the actor is in the WHERE clause, so guessing
# somebody else's token id revokes nothing. Both halves are already tested in Go
# — the store against real SQLite, the handler against the real routes over
# fakes — and this is the one place they run together, through the real binary
# against the real database.
TOKEN_ID="$(curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/profile" \
    | sed -n 's|.*action="/profile/tokens/\([^/]*\)/delete".*|\1|p' | head -1)"
[ -n "$TOKEN_ID" ] || fail "no token id on the profile page"
JAR2="$WORKDIR/cookies-intruder"
# Its own client IP: the auth limiter is per-IP, and this gate is about who owns
# the row, not about how often anybody may ask.
CODE="$(curl -s -c "$JAR2" -b "$JAR2" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/register" \
    -H 'X-Forwarded-For: 198.51.100.7' -d "name=Intruder&password=correct-horse&invite=$INVITE")"
[ "$CODE" = "303" ] || fail "the second user could not register: got $CODE, want 303"
CODE="$(curl -s -c "$JAR2" -b "$JAR2" -o /dev/null -w '%{http_code}' \
    -X POST "localhost:$PORT/profile/tokens/$TOKEN_ID/delete")"
# The same answer an already-revoked token gets: somebody else's row and a row
# that never existed are indistinguishable from outside.
[ "$CODE" = "303" ] || fail "the intruder's revoke answered $CODE, want the ordinary 303"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SECRET" "localhost:$PORT/rooms")"
[ "$CODE" = "200" ] || fail "the token answered $CODE after somebody else tried to revoke it — the WHERE clause has lost its user_id"

step "smoke: revoking a token stops it"
TOKEN_ID="$(curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/profile" \
    | sed -n 's|.*action="/profile/tokens/\([^/]*\)/delete".*|\1|p' | head -1)"
[ -n "$TOKEN_ID" ] || fail "no token id on the profile page"
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST "localhost:$PORT/profile/tokens/$TOKEN_ID/delete"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SECRET" "localhost:$PORT/rooms")"
[ "$CODE" = "401" ] || fail "a revoked token still answers $CODE — revocation is a DELETE, not a flag"

step "smoke: a reset link is delivered, works once, and ends the other sessions"
# The whole loop with no relay and no account anywhere: -mailer=log is the
# default, so an empty environment exercises request -> outbox -> ticker ->
# adapter -> the link (patterns/go-email.md). It runs against the second user,
# so the browser every gate above uses keeps its session.
curl -fsS -c "$JAR2" -b "$JAR2" -o /dev/null -X POST "localhost:$PORT/profile/email" \
    -d "email=intruder@example.com"
JAR3="$WORKDIR/cookies-reset"
# Host is whatever the client sent. A link built from it mails a working token
# to whoever asked for one — the tier-1 rule this gate exists for.
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/reset" \
    -H 'Host: evil.example' -H 'X-Forwarded-For: 198.51.100.8' -d "name=Intruder")"
[ "$CODE" = "303" ] || fail "asking for a reset link: got $CODE, want 303"

# The ticker drains the outbox every five seconds.
LINK=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
    LINK="$(sed -n 's|.*\(http://[^ "\\]*reset/confirm?t=[A-Za-z0-9_-]*\).*|\1|p' \
        "$WORKDIR/server.log" | head -1)"
    [ -n "$LINK" ] && break
    sleep 1
done
[ -n "$LINK" ] || fail "no reset link was delivered within ten seconds"
echo "$LINK" | grep -q "evil.example" && fail "the link was built from the Host header"
echo "$LINK" | grep -q "127.0.0.1:$PORT" || fail "the link was not built from the configured base URL"

TOKEN="${LINK##*t=}"
# Its own client IP throughout: the reset endpoints are rate limited per address
# like every other way in, and this gate is about the link, not about how often
# anybody may ask for one.
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' -X POST \
    "localhost:$PORT/reset/confirm" -H 'X-Forwarded-For: 198.51.100.8' \
    -d "token=$TOKEN" --data-urlencode "password=a whole new password")"
[ "$CODE" = "303" ] || fail "using the link: got $CODE, want 303"
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' "localhost:$PORT/rooms")"
[ "$CODE" = "200" ] || fail "the reset did not sign the person in: got $CODE"

# The point of resetting a password somebody else may know.
CODE="$(curl -s -c "$JAR2" -b "$JAR2" -o /dev/null -w '%{http_code}' "localhost:$PORT/rooms")"
[ "$CODE" = "303" ] || fail "the session on the other machine survived the reset: got $CODE"

# Single use, and Take spends the row in the transaction that reads it.
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' -X POST \
    "localhost:$PORT/reset/confirm" -H 'X-Forwarded-For: 198.51.100.8' \
    -d "token=$TOKEN" --data-urlencode "password=another new one")"
[ "$CODE" = "422" ] || fail "the link worked a second time: got $CODE, want 422"

step "smoke: asking for a reset says the same thing whoever asks"
# Three requests the server knows three different things about, and one answer.
# Anything that varied would make this form a way to ask which accounts exist.
for NAME in Intruder "Nobody At All" Ada; do
    curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}\n' -X POST "localhost:$PORT/reset" \
        -H 'X-Forwarded-For: 198.51.100.9' --data-urlencode "name=$NAME"
done > "$WORKDIR/reset-codes"
sort -u "$WORKDIR/reset-codes" > "$WORKDIR/reset-codes-unique"
[ "$(wc -l < "$WORKDIR/reset-codes-unique")" -eq 1 ] \
    || fail "the answer changes with who is asking: $(tr '\n' ' ' < "$WORKDIR/reset-codes")"
grep -q '^303$' "$WORKDIR/reset-codes-unique" || fail "asking for a link does not answer 303"

step "smoke: deleting an account takes the person with it"
# patterns/go-data-deletion.md, end to end through the real binary against the
# real database. The Intruder is the account this run is finished with.
#
# Neither half of the confirmation is enough on its own.
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/account/delete" \
    -d "name=Intruder" --data-urlencode "password=not the password")"
[ "$CODE" = "422" ] || fail "deleting with the wrong password answered $CODE, want 422"
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/account/delete" \
    -d "name=Somebody Else" --data-urlencode "password=a whole new password")"
[ "$CODE" = "422" ] || fail "deleting with the wrong name answered $CODE, want 422"
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' "localhost:$PORT/rooms")"
[ "$CODE" = "200" ] || fail "two refused deletes signed the person out: got $CODE"

CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/account/delete" \
    -d "name=Intruder" --data-urlencode "password=a whole new password")"
[ "$CODE" = "303" ] || fail "deleting the account answered $CODE, want 303"

# No cascade reaches a session row: it is keyed by token and its payload is
# opaque to SQL. What ends this one is authenticate resolving the credential to
# a row that is not there any more.
CODE="$(curl -s -c "$JAR3" -b "$JAR3" -o /dev/null -w '%{http_code}' "localhost:$PORT/rooms")"
[ "$CODE" = "303" ] || fail "the deleted account's cookie still opens a page: got $CODE"

# The name comes free, which is only true of a row that is gone rather than
# hidden behind a flag.
CODE="$(curl -s -c "$WORKDIR/cookies-again" -b "$WORKDIR/cookies-again" -o /dev/null -w '%{http_code}' \
    -X POST "localhost:$PORT/register" -H 'X-Forwarded-For: 198.51.100.11' \
    -d "name=Intruder&password=correct-horse&invite=$INVITE")"
[ "$CODE" = "303" ] || fail "the name did not come free after the delete: got $CODE"

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
# A fresh message rather than one from the top of this run: the room pages, so
# an early message is behind a "Show older" control by now and its absence from
# the first screen would say nothing about what survived.
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST "localhost:$PORT/rooms/general-chat/messages" \
    --data-urlencode "body=written just before the restart"
kill -TERM "$SERVER_PID" && wait "$SERVER_PID" 2>/dev/null || true
CREDENTIALS_DIRECTORY="$WORKDIR/creds" \
    "$WORKDIR/server" -port "$PORT" -ops-port "$OPS_PORT" -database-url "$WORKDIR/app.db" \
    -timezone Europe/Berlin \
    >>"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS "localhost:$OPS_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done
# The session outlives the restart too: it lives in SQLite, not in memory.
curl -fsS -c "$JAR" -b "$JAR" "localhost:$PORT/rooms/general-chat" \
    | grep -q "written just before the restart" \
    || fail "the conversation was lost after the restart"

step "smoke: graceful shutdown"
kill -TERM "$SERVER_PID"
wait "$SERVER_PID" || fail "server exited non-zero on SIGTERM"
SERVER_PID=""
grep -q "shutting down" "$WORKDIR/server.log" || fail "no graceful shutdown log line"

printf '\nAll gates passed.\n'

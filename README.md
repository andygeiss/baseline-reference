# baseline-reference

Reference implementation and **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline): **Go Chat**, a
mobile-first chat app with a command-line client, built strictly per the
baseline's `project-types/web-application.md` and `project-types/cli-tool.md`.

Two binaries, one module. The server renders HTML for people and JSON for
programs; `gochat` is the program.

- **[SPEC.md](SPEC.md)** — what this test is: the task, the pinned baseline commit,
  the acceptance criteria, and the protocol for reproducing the test from scratch.
- **[verify.sh](verify.sh)** — the mechanical acceptance run: every CI gate from the
  baseline plus a live smoke test of both built binaries. `./verify.sh` must exit 0.
- **[DESIGN.md](DESIGN.md)** — the design system: theme values and component
  inventory, lockstep with `web/static/css/app.css`.

## Stack

Go 1.26 (stdlib `net/http`, `html/template`, `log/slog`) · htmx 2.0.10 (vendored, the
only script — SHA-256 checked by verify.sh) · pure CSS (cascade layers, mobile-first
grid layout, oklch, media-query dark mode, motion-as-feedback with view-transition
swaps, system font stack, mask icons, a fixed bottom bar, one `<dialog>`) · SQLite
(`modernc.org/sqlite`, WAL, single-writer pool) · sessions (`alexedwards/scs/v2`
over a hand-written two-pool store) · argon2id (`golang.org/x/crypto`) · per-IP
rate limiting (`golang.org/x/time`) · installable (web app manifest + four icons,
no service worker) · single static binaries with all assets embedded.

Every dependency is on the approved list in the baseline's `stack/go.md`, used the
way that list prescribes, so none needs a justification here.

The five UI icons are CSS masks. Their path data comes from
[Lucide](https://lucide.dev), which ships under the ISC license, with the parts it
inherits from Feather under MIT.

## Run

```sh
make run                       # http://localhost:8080, ops on localhost:6060
make test                      # inner loop: race + shuffle, as CI runs it
make check                     # default target: every CI gate, gate-for-gate
make build                     # both binaries into bin/
./verify.sh                    # full acceptance gauntlet
```

Open the app, make an account, make a room, say something. Then make a token on
the **You** page and talk to the same rooms from a terminal:

```sh
go install github.com/andygeiss/baseline-reference/v3/cmd/gochat@latest

echo "$TOKEN" > ~/.config/gochat/token
export GOCHAT_ADDR=http://localhost:8080 GOCHAT_TOKEN_FILE=~/.config/gochat/token

gochat rooms                        # slug and name, one room per line
gochat post general "ship it"         # prints the new cursor
git log --oneline -5 | gochat post general -
gochat read -json general             # one JSON object per line
watch -n3 gochat read general         # a live tail, composed rather than built in
```

`make run` sources a `.env` when one is present (`stack/makefile.md` rule 6). This
app needs no secret to start, so the file is optional here and gitignored; `make
check` and `make test` ignore it on purpose, because a gate must not depend on one
machine.

Configuration: `HOST`, `PORT`, `OPS_PORT`, `DATABASE_URL`, and `LOG_LEVEL` are flags
with env-var defaults (the flag wins); `ENV` is read from the environment only; the
registration invite code is read from a file in `$CREDENTIALS_DIRECTORY`.
`server -h` prints the whole contract, including the environment-only variables and
the credential. The `Config` struct and its parser live in `cmd/server/config.go`,
validated before anything binds a port or opens a file — `patterns/go-config.md`.

## Two surfaces, on purpose

The pages answer in HTML because a browser renders HTML. `/api` answers in JSON
because `gochat` cannot render anything. They are separate surfaces rather than two
representations of one: nothing negotiates on `Accept`, one URL means one thing,
and the two are free to differ — `/api` has no forms, no redirects, and no flash
messages. A signed-out reader gets a redirect to the sign-in page; a program gets
401 and a JSON reason, because a 200 full of HTML reads as success to anything
checking only the status.

`internal/chatapi` is the only package that knows the server speaks HTTP at all.
It imports `internal/domain` and nothing else of ours, and verify.sh proves that
with `go list -deps` — `patterns/go-ports-adapters.md`.

## Baseline deviations

Recorded per the baseline's rules. Entries marked **waived** carry the six fields the
baseline's *Which rules can be waived* requires — rule, document, date, decider, why,
and what contains it. The rest are conformance notes and unexercised patterns, labelled
as such: a reader hunting for gaps counts every bullet here, so each one says which it
is.

- **Never deployed** (`operations/web-application.md`, and the web checklist's Ship
  section) — waived 2026-08-13 by Andy. This repo is an acceptance test, not a
  service. The binary holds up its end of that contract — the env vars, `127.0.0.1`
  by default, `/healthz` with the version, graceful shutdown, secrets from
  `$CREDENTIALS_DIRECTORY` — and `verify.sh` gates every one of them. The
  deployment's end is absent on purpose: no image, no compose file, no Caddy, no
  `GOMEMLIMIT`, and no previous version to roll back to. Those belong to the
  operations repository,
  [baseline-ops](https://github.com/andygeiss/baseline-ops), which builds its own
  template against a checkout of this repo.
- **Never released** (`operations/cli-release.md`, and the CLI checklist's Ship
  section) — waived 2026-08-15 by Andy. There is no `release.yml` and no
  cross-compiled artifact. This repository's tags mirror the baseline version it was
  built against, so a release cut from one would announce "baseline v3.2.0" rather
  than anything about the tool's own contract, and the semver promise
  `cli-release.md` asks for would be hostage to a document release. Contained by
  distribution channel 1 alone: `go install …/cmd/gochat@<tag>` works, version
  stamping is gated, and the stdout and `-json` shapes are treated as a contract in
  the code and its tests. **This is the acceptance test's one remaining coverage
  hole**, and it is a real one — nothing here exercises the artifact workflow, the
  checksums, or the six cross-compiled targets.
- **`OPS_PORT` is a config var** (`patterns/go-http-server.md`, which pins the ops
  listener to `127.0.0.1:6060` — "fixed, not a flag") — waived 2026-08-10 by Andy.
  The port is configurable so `verify.sh` can boot test instances beside a running dev
  server. The bind address stays hardcoded to localhost, so the listener is still
  unreachable from off the box.
- **A stale action returns 200 with a message, not 404** — a design decision, not a
  waiver: no baseline rule asks for 404 here. Revoking a token another tab already
  revoked is a stale page, not a bad request — the answer replaces it with current
  truth and says what happened. The 422 flow is reserved for what it targets, *form
  validation*. (htmx 2 also doesn't swap 4xx responses by default.)
- **The invite code is a feature written to exercise a pattern** — said plainly
  because it is true. `patterns/go-config.md` §Secrets had no customer in this app
  until registration grew a gate, so the gate exists partly to give
  `$CREDENTIALS_DIRECTORY`, `readCredential`, and the `LogValue` allowlist something
  real to protect. It is a plausible feature — it keeps a public demo from being
  spammed — and it is gated end to end: verify.sh boots the binary with a credential
  file, proves a wrong code is refused, and proves the code never reaches the log.
- **Only the part of the type scale the app renders** (`patterns/css-typography.md`):
  the UI has two heading levels, so `app.css` carries the `h1` and `h2` steps and not
  `h3`–`h6`. `small`, `code`, and the table rules all ship. The scale's rules hold:
  no root `font-size`, no size tokens, and a `rem` term in every `clamp()`, which
  verify.sh gates. No web font: the system stack is the pattern's default answer,
  not a waiver.
- **The library project type is unexercised** (`project-types/library.md`,
  `patterns/go-library.md`, `checklists/library.md`) — and for the reason that
  document itself gives. Libraries are *extracted when a second project needs the
  code*; here the second consumer, `cmd/gochat`, lives in the same module, so
  `internal/` is exactly where the shared code belongs. Extracting it to earn a
  checkmark would be the ceremony that document warns about. The first genuine
  second consumer changes this.

## Fed back into the baseline

Findings from this repo that the baseline has since adopted — the reproduction
protocol working as intended. Kept as a record; none of them is a deviation anymore.

- **`img-src 'self' data:` in the CSP** → baseline v1.14.0. Mask icons are data URIs
  and a CSS image is an image request, so `default-src 'self'` alone made Chrome
  refuse every mask. The directive now lives in `patterns/security-headers.md`, which
  owns the whole policy, and the checklist gates it.
- **`fs.Usage` naming the environment-only variables** → baseline v1.14.2. `ENV` is
  read from the environment and appears in no flag's help text, so `-h` was a partial
  contract until the usage function named it. `patterns/go-config.md` rule 5 always
  required this; its canonical snippet did not implement it.
- **The module path carries the major version** → baseline v3.0.1. §Version stamping
  promised `info.Main.Version` is "the tag when HEAD sits on one"; past v1 that holds
  only when `go.mod` ends in the matching `/vN`. On its first v3 tag, this repository
  built a binary reporting `v1.17.1-0.20260815164832-b67cd862f0fb`.
- **Exit 2 for a configuration error** → baseline v1.14.2. `cmd/server/main.go` exits
  2 on any `parseConfig` failure, where `patterns/go-cli.md`'s `default` branch exits
  1. The baseline now states the divergence and why a config error is always a usage
  error rather than presenting the two switches as identical.
- **A `Secure` session cookie cannot be exercised over the HTTP this baseline
  mandates** → the sync that produced this app. `patterns/go-auth-sessions.md` sets
  `sessions.Cookie.Secure = true` flat, and `project-types/web-application.md` says
  the binary only ever speaks plain HTTP behind a TLS proxy. A cookie marked `Secure`
  is never sent back over `http://`, so `make run` cannot sign anybody in and
  `verify.sh` cannot reach a single authenticated route. Two rules that are each
  correct and do not compose — which is the class of defect only a running
  application finds. Here the flag follows `ENV`, which the deployment sets and which
  is the thing that knows whether TLS is in front: `Secure` in production, off in
  dev, `HttpOnly` and `SameSite=Lax` always.
- **One authenticator, two middlewares, two credential failures** → the same sync.
  `patterns/go-auth-sessions.md` said `requireAuth` takes either credential and
  stopped there. Building a second surface showed what it left out: the lookup is
  shared, but the answer to "nobody is signed in" cannot be, because a 303 to a
  sign-in page is 200 of HTML that reads as success to a program. And a token
  *presented and refused* is not the same as none presented — treating a revoked
  token as "signed out" hides the revocation behind a login form. Both are now rules
  in that document.
- **htmx stops polling on 286 only while `responseHandling` counts 286 as a swap** →
  the same sync. Traced through the vendored htmx 2.0.10: the cancel sits inside the
  swap branch, so the canonical `htmx-config` meta stops polling through its
  `{"code":"[23]..","swap":true}` rule, by luck rather than by design. Narrowing that
  pattern to `2..` — which reads more precise — would leave a poll running forever
  with nothing in the console to say so.

## Beyond the baseline

Things this implementation adds that the baseline does not spell out. Each is a
candidate to feed back into it, per the reproduction protocol in [SPEC.md](SPEC.md).

- **A form-level error, beside the per-field ones** (`internal/app/forms.go`). The
  baseline's `Validator` carries `FieldErrors` only. Sign-in needs a failure that
  belongs to no single field: "We do not know that name and password" cannot be
  attached to either box, because saying which half was wrong is exactly what tells
  an attacker which names exist.
- **The page list comes from the directory** (`internal/app/app.go`). A hand-kept
  list of page templates is one a new page gets left out of, and the symptom is a
  500 from one route while every test of every other route stays green. This
  repository shipped exactly that bug for an afternoon.
- **Reads are retried and writes are not** (`internal/chatapi/client.go`). The
  baseline's retry policy is about which status codes deserve another attempt. The
  rule underneath it is which *methods* do: a POST whose answer got lost may already
  have reached the server, and retrying it says the same thing twice in the room.
- **A name may be two characters** (`internal/domain/user.go`). A three-character
  floor is an English-nickname number, and it refuses 李雷 — a whole name in plenty
  of scripts.
- **Flags before positionals, said out loud** (`cmd/gochat/run.go`). Go's `flag` stops
  at the first plain argument, so `gochat read general -json` silently treats the flag
  as an extra argument. The tool spots that shape and says which order to use
  instead of complaining that a room is missing from a command line that has one.

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
- **[GLOSSARY.md](GLOSSARY.md)** — the words this project owns: one per concept,
  the runners-up under _Avoid_.

## Stack

Go 1.26 (stdlib `net/http`, `html/template`, `log/slog`) · htmx 2.0.10 (vendored, the
only script — SHA-256 checked by verify.sh) · pure CSS (cascade layers, mobile-first
grid layout, oklch, media-query dark mode, motion-as-feedback with view-transition
swaps, system font stack, mask icons, a fixed bottom bar, one `<dialog>`) · SQLite
(`modernc.org/sqlite`, WAL, single-writer pool, attachments as `BLOB`s) · sessions
(`alexedwards/scs/v2` over a hand-written two-pool store) · argon2id
(`golang.org/x/crypto`) · per-IP rate limiting (`golang.org/x/time`) · mail over
stdlib `net/smtp`, or a log adapter that needs nothing · the time zone database
embedded with `time/tzdata` · installable (web app manifest + four icons, no service
worker) · single static binaries with all assets embedded.

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
make lan                       # https://<your-mac>.local:8443 for a phone (see below)
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

Attach a file to a message and it comes back through a handler, never a file server:
the type is whatever the bytes turn out to be, and only the pictures this app renders
are shown in the page — everything else downloads. Press **Show older** at the top of
a long room to walk backwards through it.

To try a password reset, put an address on the **You** page and ask for a link. With
no relay configured the default mailer writes the whole message to the log, so the
link is in `make run`'s output within five seconds. That adapter is refused when
`ENV=prod`, because a log is not an inbox.

Configuration: `HOST`, `PORT`, `OPS_PORT`, `DATABASE_URL`, `LOG_LEVEL`, `BASE_URL`,
`TIMEZONE`, `MAILER`, and the three `SMTP_*` settings are flags with env-var defaults
(the flag wins); `ENV` is read from the environment only; the registration invite code
and the SMTP password are read from files in `$CREDENTIALS_DIRECTORY`. `server -h`
prints the whole contract, including the environment-only variables and the
credentials. The `Config` struct and its parser live in `cmd/server/config.go`,
validated before anything binds a port or opens a file — `patterns/go-config.md`.
`BASE_URL` is the one an emailed link is built from, and it is never taken from the
request.

### Reaching it from a phone

**This project opts into local HTTPS** (`patterns/local-https.md`). It is installable,
and a browser only offers install over HTTPS. Install is therefore the one feature
here that a phone cannot try over a plain LAN address, however correct the manifest
is — and it is the only secure-context feature this app has.

```sh
make run    # the app, on 127.0.0.1:8080
make lan    # in a second shell: Caddy on https://<your-mac>.local:8443
```

Caddy is a system binary rather than a `go run` tool, so install it first
(`brew install caddy`) — `make lan` is the one target here that needs something the
Go toolchain does not bring. The operations repository pins the version; this
repository deliberately does not pin a second one.

Then trust Caddy's root on the device — the three iOS steps, including the
**Certificate Trust Settings** toggle people miss, are in the pattern. `sudo caddy
trust` does the same for this machine's own browser.

The binary is untouched by any of it: it speaks plain HTTP on loopback, and Caddy in
front terminates TLS — the same shape as production, on a different machine.
`Caddyfile.lan` is a separate artefact from the deployment Caddyfile, which this
repository does not have at all (see the *Never deployed* waiver below): a `.local`
name, a local authority, and a loopback upstream. Nothing here reaches a server, and
the root certificate it generates is never committed.

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

## Who owns what

`patterns/go-authorization.md` makes a project say which rows have an owner, because a
missing predicate and a forgotten one look identical in a diff.

| Table | Owner | What that means here |
|---|---|---|
| `tokens` | whoever created it | Every query names the actor. `ByUser` lists only theirs; `Delete` carries `user_id` in the `WHERE` clause and checks how many rows it hit, so guessing somebody else's token ID revokes nothing. `TestRevokingSomebodyElsesTokenDoesNothing` is that check. |
| `users` | itself | `/profile` reads the signed-in user from the session. No route takes a user ID, so there is nothing to guess. |
| `rooms` | **nobody — shared on purpose** | This is a group chat: every signed-in member sees every room and may post in any of them. `All` and `BySlug` take no actor because there is no owner to match, not because one was forgotten. |
| `messages` | **nobody — shared on purpose** | A message belongs to its room, and rooms are shared. Messages are never edited or deleted, so the only write is an append that stamps its author from the credential rather than from the request. |
| `attachments` | **shared to read, owned to delete** | Reading follows the message it hangs on, so `Open` takes no actor: everyone who can read the room can open the file. Deleting does not. `Delete` carries `uploader_id` in the `WHERE` clause and checks how many rows it hit, so somebody else's file answers exactly like one that was never there. `TestOnlyTheUploaderRemovesAFile` is that check. |
| `resets` | whoever asked for it | Never listed and never rendered. The row is found by the SHA-256 of the token in the emailed link and spent in the transaction that reads it, so a link works once. |
| `outbox` | **nobody — machinery** | One background sender drains it. No route reads it and no page renders it. |

Adding membership would change the first column, not the shape: which rooms a user may see
becomes a query with the actor in it, and `All` grows a parameter.

Route protection is not per-handler here either. `internal/app/routes.go` holds one table
whose rows are positional literals, so a route that names no access class does not
compile, and `guard` panics at boot on a class it does not know rather than falling back
to public. `TestPrivateRoutesTurnAwayAnAnonymousRequest` walks that table instead of a
hand-kept list of paths.

## Decisions the baseline makes a project name

Three patterns end by asking for an answer in the README rather than handing out a
default. Here they are, in one place.

- **Time zone: one for the whole app** (`patterns/time-and-dates.md`). No JavaScript
  means no browser clock and no browser zone, so the server picks. Every moment is
  rendered in the zone the deployment names — `-timezone`, `Europe/Berlin` in the
  acceptance run — with its abbreviation beside it, and carries the exact UTC value in
  `<time datetime>`. A per-reader setting would be one more column and one more page,
  and a group chat with one clock does not need it. Nothing calls `Format` outside
  `newStamp`, and no template is ever handed a `time.Time`.
- **A send snaps the room back to the newest page** (`patterns/htmx-lists.md`).
  Posting re-renders the whole chat region, so somebody who had pressed "Show older"
  is at the bottom again afterwards. For a chat that is what pressing Send is expected
  to do. A list people work through rather than talk in would append at the arrival
  end instead and leave the loaded pages alone.
- **Attachment bytes live in the database** (`patterns/go-file-uploads.md`). The
  backup that already protects the messages protects their files, `VACUUM INTO` sees
  everything, and a row and its file cannot disagree — which is why this app has no
  orphan sweeper. The cost is the 2 MB cap: the right size for a screenshot or a log,
  the wrong size for video.

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
  deployment's end is absent on purpose: no image, no compose file, no deployment
  Caddyfile, no `GOMEMLIMIT`, and no previous version to roll back to. Those belong
  to the operations repository,
  [baseline-ops](https://github.com/andygeiss/baseline-ops), which builds its own
  template against a checkout of this repo. The root `Caddyfile.lan` is not a
  counter-example and does not narrow this waiver: it is the local-HTTPS artefact
  from `patterns/local-https.md`, it runs on a developer's machine, and rule 3 of
  that pattern forbids it reaching a server at all.
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
- **The assistant calls the API directly, with no SDK** (`patterns/go-llm-adapter.md`
  rule 20) — a conformance note, recorded because that rule asks for it either way.
  A reply to a conversation is one endpoint and a handful of fields, which is the
  document's own stdlib case: it adds no dependency, `stack/go.md` has no Anthropic
  SDK on its approved list, and the wire-contract test in `internal/anthropic` is
  what keeps it honest. The choice departs from the `claude-api` skill's own default
  of reaching for the SDK, which is the half being recorded here. Streaming, a
  tool-use loop, or structured outputs would flip it — and would need the SDK
  justified in this README instead.
- **The assistant replies inside the request** — a conformance note.
  `handleMessagePost` persists the message (required), then asks the model
  (enhancement) before answering, so a mention costs the sender the model's latency.
  A production chat would answer first and let the existing poll deliver the reply.
  Synchronous is what makes the ordering in `patterns/go-errors-logging.md` visible
  in one function and gives the timeout ladder something real to bound: a 10s handler
  budget, a 15s client timeout, a 30s `WriteTimeout`.
- **`internal/echo` is a product mode, not a test double**
  (`patterns/go-llm-adapter.md` rule 14) — a conformance note. It is the default,
  which is what lets this app start with an empty environment and still exercise the
  whole loop. It lives in `internal/` and config selects it; the fake that tests the
  handlers is a separate thing in `internal/app/fakes_test.go`.
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
- **A `Secure` session cookie does not survive the HTTP this baseline mandates**
  → baseline v3.3.1. `patterns/go-auth-sessions.md` set `sessions.Cookie.Secure = true`
  flat, and `project-types/web-application.md` says the binary only ever speaks plain
  HTTP behind a TLS proxy. Two rules that are each correct and do not compose — the
  class of defect only a running application finds. It took two goes to state
  correctly, because the first telling was measured against nothing: loopback *is* a
  secure context, so `curl` and a browser both return the flagged cookie over
  `http://localhost`, and the flat flag looks fine on a laptop and in `verify.sh`.
  Over a LAN address the cookie is not stored at all — a phone checking the
  mobile-first layout, a container reached by hostname, and a plain-HTTP staging box
  each fail to sign anybody in. Here the flag follows `ENV`, which the deployment sets
  and which is the thing that knows whether TLS is in front: `Secure` in production,
  off in dev, `HttpOnly` and `SameSite=Lax` always.
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
- **`ParseForm` silently empties a multipart body** → the upload pattern's rule 1.
  On a multipart request it leaves `PostForm` non-nil and empty, so every
  `PostFormValue` after it answers `""` — the field is on the wire and gone in Go,
  with no error anywhere. `FormFile` is the same shape from the other side: on a plain
  urlencoded post it answers `http.ErrNotMultipart`, which means "no file attached",
  not "something went wrong". Both cost this repository a round of 500s.
- **"Assert the upload is refused" was the wrong test** → the same sweep. An allowlist
  with `text/plain` on it stores a lying SVG as text and hands it back as an
  attachment, which is safe; an allowlist without it stores nothing. Both are correct
  and only one is "refused", so the rule now asks what a file is never *served as*.
- **A paged chat is allowed to snap back to the newest page on Send** → the same
  sweep. The list pattern forbade the whole-region swap outright, which is right for a
  list somebody works through and wrong for one they talk in — pressing Send in a chat
  is expected to take you to the bottom. The rule now names the trade and asks for the
  answer in the README.

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

# baseline-reference

Reference implementation and **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline): a mobile-first todo
app — a simple way to organize tasks — as a server-rendered web application, built
strictly per the baseline's `project-types/web-application.md`.

- **[SPEC.md](SPEC.md)** — what this test is: the task, the pinned baseline commit,
  the acceptance criteria, and the protocol for reproducing the test from scratch.
- **[verify.sh](verify.sh)** — the mechanical acceptance run: every CI gate from the
  baseline plus a live smoke test of the built binary. `./verify.sh` must exit 0.
- **[DESIGN.md](DESIGN.md)** — the design system: theme values and component
  inventory, lockstep with `web/static/css/app.css`.

## Stack

Go 1.26 (stdlib `net/http`, `html/template`, `log/slog`) · htmx 2.0.10 (vendored, the
only script — SHA-256 checked by verify.sh) · pure CSS (cascade layers, mobile-first
grid layout, oklch, media-query dark mode, motion-as-feedback with view-transition
swaps, system font stack, mask icons) · SQLite (`modernc.org/sqlite`, WAL,
single-writer pool) · installable (web app manifest + four icons, no service
worker) · single static binary with all assets embedded.

The four UI icons are CSS masks. Their path data comes from
[Lucide](https://lucide.dev), which ships under the ISC license, with the parts it
inherits from Feather under MIT.

## Run

```sh
make run                       # http://localhost:8080, ops on localhost:6060
make test                      # inner loop: race + shuffle, as CI runs it
make check                     # default target: every CI gate, gate-for-gate
./verify.sh                    # full acceptance gauntlet
```

`make run` sources a `.env` when one is present (`stack/makefile.md` rule 6). This
app needs no secret, so the file is optional here and gitignored; `make check` and
`make test` ignore it on purpose, because a gate must not depend on one machine.

Configuration: `HOST`, `PORT`, `OPS_PORT`, `DATABASE_URL`, and `LOG_LEVEL` are flags
with env-var defaults (the flag wins); `ENV` is read from the environment only.
`server -h` prints the whole contract, including the environment-only variables.
The `Config` struct and its parser live in `cmd/server/config.go`, validated
before anything binds a port or opens a file — `patterns/go-config.md`.

## Baseline deviations

Recorded per the baseline's rules. Entries marked **waived** carry the six fields the
baseline's *Which rules can be waived* requires — rule, document, date, decider, why,
and what contains it. The rest are conformance notes and unexercised patterns, labelled
as such: a reader hunting for gaps counts every bullet here, so each one says which it
is.

- **No auth/sessions** (`patterns/go-auth-sessions.md`) — waived 2026-08-10 by Andy.
  The app has no user accounts, so there is **one list, shared by everyone who opens
  the page**. That is honest for an acceptance test and wrong for a real todo app —
  the first feature that needs a private list adopts the whole pattern.
- **No flash messages** (`patterns/go-forms-validation.md` §Flash) — waived 2026-08-15
  by Andy. Flash requires sessions (waived above), so results render on the target
  page, as that pattern prescribes for session-less apps. One consequence: with htmx switched off,
  "That task is gone." is lost across the redirect, and the reader sees only the
  healed list. The rest of the pattern — the validator, the 422 re-render with the
  submitted value kept, `HX-Push-Url: false` on a boosted 422 — is implemented.
- **No backups** — and since baseline v3.0.0 that is conformance, not a waiver.
  `patterns/go-sqlite.md` §Backups now asks one question, *if the server disappears
  right now, what have you lost?*, and offers three legitimate answers. This app's is
  the first row, "nothing that matters": the data is throwaway demo data, so the
  mechanism is nothing and the obligation is to record the decision here, which this
  bullet is. Everything else in that document (pragmas, pools, migrations) is
  implemented.
- **Never deployed** (`operations/web-application.md`, and the checklist's Ship
  section) — waived 2026-08-13 by Andy. This repo is an acceptance test, not a
  service. The binary holds up its end
  of that contract — the env vars, `127.0.0.1` by default, `/healthz` with the version,
  graceful shutdown — and `verify.sh` gates every one of them. The deployment's end is
  absent on purpose: no image, no compose file, no Caddy, no `GOMEMLIMIT`, and no
  previous version to roll back to. Those belong to the operations repository,
  [baseline-ops](https://github.com/andygeiss/baseline-ops), and this repository
  stopped carrying copies of its templates when the baseline split operations out. A
  copy that nothing builds only drifts, which is exactly what the `Dockerfile` here had
  done; baseline-ops builds its own template against a checkout of this repo instead.
- **Acting on a task that is gone returns 200 with a message, not 404** — a design
  decision, not a waiver: no baseline rule asks for 404 here. Ticking off
  or deleting a task another tab already removed is a stale list, not a bad request —
  the response replaces the stale list with current truth and says what happened. The
  422 flow is reserved for what it targets, *form validation*, and the add form uses
  it. (htmx 2 also doesn't swap 4xx responses by default.)
- **`OPS_PORT` is a config var** (`patterns/go-http-server.md`, which pins the ops
  listener to `127.0.0.1:6060` — "fixed, not a flag") — waived 2026-08-10 by Andy.
  The port is configurable so `verify.sh` can boot test instances beside a running dev
  server. The bind address stays hardcoded to localhost, so the listener is still
  unreachable from off the box.
- **Only the part of the type scale the app renders** (`patterns/css-typography.md`):
  the UI has one heading level and no `<small>`, code, or tables, so `app.css`
  carries the `h1` step alone. `font: inherit` sits on `button` and on the one text
  field. The scale's rules still hold: no root `font-size`, no size tokens,
  and a `rem` term in every `clamp()`, which verify.sh gates. No web font: the
  system stack is the pattern's default answer, not a waiver.
- **No outbound HTTP** (`patterns/go-http-client.md` not exercised): the app calls
  no external API, so nothing here builds an `http.Client`. The pattern is
  unexercised rather than waived — the first feature that needs a third-party call
  adopts it whole. **This is the acceptance test's one real coverage hole**, and
  v1.14.2 fixed a genuine bug in that pattern (a retry that resent an unreplayable
  body as an empty one) which this repo could not have caught. That fix was verified
  by compiling the pattern's snippet standalone against an `httptest.Server`; until
  some reference app makes an outbound call, go-http-client has no continuous gate.
- **No secrets** (`patterns/go-config.md` §Secrets not exercised): nothing in this
  app needs a credential, so there is no `LoadCredential` and no
  `$CREDENTIALS_DIRECTORY` read. `Config.LogValue` is implemented anyway, listing
  every field — the allowlist shape is what keeps a future secret out of the logs
  by default.
- **No `<dialog>` in the UI** (`patterns/css-motion.md` dialog entry not exercised):
  the app has no modal; deleting a task asks with `hx-confirm`, which is the
  browser's own dialog. The rest of the pattern is implemented — motion tokens,
  base-layer state transitions, the indicator fade with its 100 ms entry delay,
  view-transition swaps (list changes opt out as rapid-fire controls, swap rule 1),
  and the reduced-motion kill switch.
- **No bottom navigation** (`patterns/css-layout.md` §Bottom navigation not
  exercised): the app is one page, and a bar with one destination navigates nothing.

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
  only when `go.mod` ends in the matching `/vN`. Without the suffix Go refuses the tag
  for the main module and says nothing, stamping a pseudo-version off the last v1 tag
  instead. On its first v3 tag, this repository built a binary reporting
  `v1.17.1-0.20260815164832-b67cd862f0fb` — and baseline-ops' templates gate read
  exactly that off the running container, which is the acceptance test and the
  operations gate catching the same defect from two directions. The path is now
  `github.com/andygeiss/baseline-reference/v3`; the rule landed in
  `operations/web-application.md`, `patterns/go-cli.md`, `operations/cli-release.md`,
  and the checklist's Ship section.
- **Exit 2 for a configuration error** → baseline v1.14.2. `cmd/server/main.go` exits
  2 on any `parseConfig` failure, where `patterns/go-cli.md`'s `default` branch exits
  1. The baseline now states the divergence and why a config error is always a usage
  error rather than presenting the two switches as identical.

## Beyond the baseline

Four things this implementation adds that the baseline does not spell out. Each is a
candidate to feed back into it, per the reproduction protocol in [SPEC.md](SPEC.md).

- **A change is one write transaction** (`internal/store/tasks.go`). The pattern's
  handler reads the row, applies the rule, then saves it. Two taps that arrive
  together read the same task, and the second save undoes the first. The store now
  takes the change as a function and does read, apply, and write inside one
  transaction on the single-writer pool.
- **The status line sits outside the swapped region** (`web/templates/tasks.html`).
  A screen reader does not reliably announce a live region that arrives with its
  text already in it. So the region stays put and the response fills it out of
  band (`hx-swap-oob="innerHTML:#status"`).
- **Static directory URLs answer 404** (`internal/app/routes.go`). `FileServerFS`
  otherwise renders a browsable index of the embedded tree — an HTML page that no
  page links to, served outside the security headers and cached for a year.
- **List order comes from a counter, not a clock**
  (`internal/store/migrations/0001_create_tasks.sql`). Two tasks added in the same
  second sort at random by `created_at`, and a second-resolution timestamp is what
  `go-sqlite.md` stores. An `AUTOINCREMENT` column orders them by insert, and never
  hands a number back out, so deleting the last task cannot reorder the next one.

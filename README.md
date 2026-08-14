# baseline-reference

Reference implementation and **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline): two-player tic-tac-toe
as a server-rendered web application, built strictly per the baseline's
`project-types/web-application.md`.

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

The one UI icon is a CSS mask. Its path data comes from
[Lucide](https://lucide.dev), which ships under the ISC license, with the parts it
inherits from Feather under MIT.

## Run

```sh
make run                       # http://localhost:8080, ops on localhost:6060
make test                      # inner loop: race + shuffle, as CI runs it
make check                     # default target: every CI gate, gate-for-gate
./verify.sh                    # full acceptance gauntlet
```

Configuration: `HOST`, `PORT`, `OPS_PORT`, `DATABASE_URL`, and `LOG_LEVEL` are flags
with env-var defaults (the flag wins); `ENV` is read from the environment only.
`server -h` prints the whole contract, including the environment-only variables.
The `Config` struct and its parser live in `cmd/server/config.go`, validated
before anything binds a port or opens a file — `patterns/go-config.md`.

## Baseline deviations

Recorded per the baseline's rules:

- **No auth/sessions** (`patterns/go-auth-sessions.md` skipped): the game has no user
  accounts. Games are addressed by unguessable IDs only.
- **No form validator or flash messages** (`patterns/go-forms-validation.md` not
  exercised): no form takes typed input — every POST is a bare button or a board
  cell, and the domain rules decide legality. Flash also requires sessions (skipped
  above); results render on the target page, as that pattern prescribes for
  session-less apps.
- **No backups/Litestream** (`patterns/go-sqlite.md` §Backups waived): throwaway demo
  data. Everything else in that document (pragmas, pools, migrations) is implemented.
- **Never deployed** (`operations/web-application.md` and the checklist's Ship section
  waived): this repo is an acceptance test, not a service. The binary holds up its end
  of that contract — the env vars, `127.0.0.1` by default, `/healthz` with the version,
  graceful shutdown — but there is no Caddy, no systemd unit, no `GOMEMLIMIT`, and no
  previous binary to roll back to.
- **Rule-violation moves return 200 with a message, not 422:** the 422 flow in the
  baseline targets *form validation*; a stale-board click is not invalid input — the
  response replaces the stale board with current truth. (htmx 2 also doesn't swap
  4xx responses by default.)
- **`OPS_PORT` is a config var**, though `patterns/go-http-server.md` pins the ops
  listener to `127.0.0.1:6060` ("fixed, not a flag"): the port is configurable so
  `verify.sh` can boot test instances beside a running dev server. The bind address
  stays hardcoded to localhost.
- **Only the part of the type scale the app renders** (`patterns/css-typography.md`):
  the UI has one heading level and no `<small>`, code, or tables, so `app.css`
  carries the `h1` step alone. `font: inherit` sits on `button` — the only form
  control here. The scale's rules still hold: no root `font-size`, no size tokens,
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
  the game has no modal. The rest of the pattern is implemented — motion tokens,
  base-layer state transitions, the indicator fade with its 100 ms entry delay,
  view-transition swaps (board moves opt out as rapid-fire controls), and the
  reduced-motion kill switch.

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
- **Exit 2 for a configuration error** → baseline v1.14.2. `cmd/server/main.go` exits
  2 on any `parseConfig` failure, where `patterns/go-cli.md`'s `default` branch exits
  1. The baseline now states the divergence and why a config error is always a usage
  error rather than presenting the two switches as identical.

## Beyond the baseline

Four things this implementation adds that the baseline does not spell out. Each is a
candidate to feed back into it, per the reproduction protocol in [SPEC.md](SPEC.md).

- **A move is one write transaction** (`internal/store/games.go`). The pattern's
  handler reads the game, applies the rule, then saves it. Two clicks that arrive
  together read the same board, and the second save erases the first move — it
  happened in 25 of 30 tries before the fix. The store now takes the change as a
  function and does read, apply, and write inside one transaction on the
  single-writer pool.
- **The status line sits outside the swapped board** (`web/templates/game.html`).
  A screen reader does not reliably announce a live region that arrives with its
  text already in it. So the region stays put and the move response fills it out of
  band (`hx-swap-oob="innerHTML:#status"`).
- **Static directory URLs answer 404** (`internal/app/routes.go`). `FileServerFS`
  otherwise renders a browsable index of the embedded tree — an HTML page that no
  page links to, served outside the security headers and cached for a year.
- **Cell labels count from 1** (`web/templates/game.html`). Board indices start at
  zero; what a player hears should not. A one-line `inc` template function does it.

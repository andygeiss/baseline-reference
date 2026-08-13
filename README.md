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
- **No `<dialog>` in the UI** (`patterns/css-motion.md` dialog entry not exercised):
  the game has no modal. The rest of the pattern is implemented — motion tokens,
  base-layer state transitions, the indicator fade with its 100 ms entry delay,
  view-transition swaps (board moves opt out as rapid-fire controls), and the
  reduced-motion kill switch.

## Beyond the baseline

Five things this implementation adds that the baseline does not spell out. Each is a
candidate to feed back into it, per the reproduction protocol in [SPEC.md](SPEC.md).

- **The CSP carries `img-src 'self' data:`** (`internal/app/middleware.go`). Without
  it no icon paints. Mask icons are data URIs, a CSS image is an image request, and
  `'self'` never matches `data:`, so the baseline's exact header (`default-src
  'self'; frame-ancestors 'none'`) makes Chrome refuse the mask: *"Loading the image
  'data:image/svg+xml,…' violates the following Content Security Policy directive.
  The action has been blocked."* `patterns/css-icons.md` and the checklist's CSP line
  both need this directive.
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

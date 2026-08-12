# baseline-reference

Reference implementation and **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline): two-player tic-tac-toe
as a server-rendered web application, built strictly per the baseline's
`project-types/web-application.md`.

- **[SPEC.md](SPEC.md)** — what this test is: the task, the pinned baseline commit,
  the acceptance criteria, and the protocol for reproducing the test from scratch.
- **[verify.sh](verify.sh)** — the mechanical acceptance run: every CI gate from the
  baseline plus a live smoke test of the built binary. `./verify.sh` must exit 0.

## Stack

Go 1.26 (stdlib `net/http`, `html/template`, `log/slog`) · htmx 2.0.10 (vendored, the
only script — SHA-256 checked by verify.sh) · pure CSS (cascade layers, mobile-first
grid layout, oklch, media-query dark mode, motion-as-feedback with view-transition
swaps) · SQLite (`modernc.org/sqlite`, WAL, single-writer pool) ·
single static binary with all assets embedded.

## Run

```sh
make run                       # http://localhost:8080, ops on localhost:6060
make test                      # inner loop: race + shuffle, as CI runs it
make check                     # default target: every CI gate, gate-for-gate
./verify.sh                    # full acceptance gauntlet
```

Configuration (flags override env): `HOST`, `PORT`, `OPS_PORT`, `DATABASE_URL`, `LOG_LEVEL`, `ENV`.

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
- **No `<dialog>` in the UI** (`patterns/css-motion.md` dialog entry not exercised):
  the game has no modal. The rest of the pattern is implemented — motion tokens,
  base-layer state transitions, the indicator fade with its 100 ms entry delay,
  view-transition swaps (board moves opt out as rapid-fire controls), and the
  reduced-motion kill switch.

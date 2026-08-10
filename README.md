# tictactoe

Two-player tic-tac-toe as a server-rendered web application. This project is the
reference implementation / first consumer of the
[engineering baseline](https://github.com/andygeiss/baseline) — every stack choice,
pattern, and convention here follows `project-types/web-application.md` there.

## Stack

Go 1.26 (stdlib `net/http`, `html/template`, `log/slog`) · htmx 2.0.9 (vendored, the
only script) · pure CSS (cascade layers, oklch, `light-dark()`) · SQLite
(`modernc.org/sqlite`, WAL, single-writer pool) · single static binary with all
assets embedded.

## Run

```sh
go run ./cmd/server            # http://localhost:8080, ops on localhost:6060
go test -race ./...
CGO_ENABLED=0 go build ./cmd/server
```

Configuration (flags override env): `PORT`, `OPS_PORT`, `DATABASE_URL`, `LOG_LEVEL`, `ENV`.

## Baseline deviations

Recorded per the baseline's rules:

- **No auth/sessions** (`patterns/go-auth-sessions.md` skipped): the game has no user
  accounts. Games are addressed by unguessable IDs only.
- **No backups/Litestream** (`patterns/go-sqlite.md` §Backups waived): throwaway demo
  data. Everything else in that document (pragmas, pools, migrations) is implemented.
- **Rule-violation moves return 200 with a message, not 422:** the 422 flow in the
  baseline targets *form validation*; a stale-board click is not invalid input — the
  response replaces the stale board with current truth. (htmx 2 also doesn't swap
  4xx responses by default.)

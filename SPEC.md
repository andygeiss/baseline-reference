# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade application?*

## Baseline pin

Built against baseline commit **`6015026`**, `go fix` joining the gates: **a rewrite to
current idiom that is still pending is a red gate.** `go fix -diff ./...` is the third
line of `check` and `go fix ./...` the last of `fmt`, after `goimports`, because `go fix`
type-checks and manages its own imports. The fixers ship with the toolchain, so the pin
decides what the gate demands; a fix judged wrong is switched off by name, in the commit
that says why.

**What moved here.** The Makefile carries both lines and `verify.sh` the gate, as its
third step. The gate was red on the tree it inherited: `internal/app/messages.go` counted
the assistant's reply with `Add(1)`/`go`/`Done()`, which the pin rewrites to
`a.running.Go`, and a test drained the outbox with a three-clause loop. Two more sites —
`internal/app/attachments.go` and `auth.go` — tested for `*http.MaxBytesError` with
`errors.As` and a variable read nowhere; the next major's `errorsastype` fixer will demand
`errors.AsType`, a Go 1.26 API, so they use it now. Nothing here needed a fix switched
off.

**What the run proved.** `GOTOOLCHAIN=go1.26.7 ./verify.sh`: 77 gates, exit 0 — one more
than v4.1.0, the `go fix -diff` step. `GOTOOLCHAIN=go1.26.7 make ci` is green on this
commit, its first line `go version go1.26.7 darwin/arm64`. Go 1.27.0 is on this machine
and the baseline adopts a major at its first patch; under it the gate is red on the two
`errors.As` sites the pin passes, which is the mismatch the gate now makes visible.

## The task (give this to the builder, human or AI, verbatim)

> Build **Go Chat** — a mobile-first chat application with a command-line client —
> by following `project-types/web-application.md` and `project-types/cli-tool.md`
> of the engineering baseline. Use only what the baseline mandates or approves;
> record every deviation in the README as the baseline requires.
>
> Functional requirements:
> 1. A person makes an account and signs in. Registration may be gated by an
>    invite code the deployment supplies as a credential file.
> 2. Rooms are listed on one page and created from it. A room has an address
>    derived from its name.
> 3. A room page shows what was said and a box to say something. New messages
>    from other people appear without the reader doing anything.
> 4. Every interaction works with htmx disabled (plain forms, full-page renders).
>    Without htmx, "appears by itself" degrades to reloading the page.
> 5. A person can make tokens for programs, see when each was last used, and
>    revoke one. A token is shown once.
> 6. A `gochat` command lists rooms, reads a room, and posts to it, using a token.
>    It starts, does its job, and exits.
> 7. The conversation survives a server restart.
> 8. An assistant answers when a message mentions it, and stays out of the way
>    otherwise. It runs with no API key and no model by default, so the whole
>    loop can be exercised on an empty environment; a deployment that wants a
>    real model selects it and supplies the credential as a file.

## Acceptance criteria

1. `./verify.sh` exits 0 — it runs every mechanical gate of the baseline's `check`
   recipe (`stack/makefile.md`, explained in `operations/ci.md`) **plus** a live smoke
   test of both running binaries (health endpoint, CSP header, the invite-code gate
   and the secret staying out of the logs, session cookie flags, token renewal on
   sign-in, rate limiting, plain-form and htmx flows, the poll's 204 and 200 answers,
   escaping, the 422 validation answer, machine tokens end to end, CSRF rejection, the
   backup snapshot, state across a restart, graceful shutdown, and the `gochat` client
   talking to all of it).
2. The baseline's `checklists/web-application.md` and `checklists/cli-tool.md`
   both walk clean, with deviations waived in the README.
3. The vendored htmx file matches the version pinned in the baseline's
   `VERSIONS.md` (verify.sh checks its SHA-256).
4. `go list -deps` on each adapter — `./internal/chatapi` and
   `./internal/anthropic` — names `internal/domain` and nothing else of ours: an
   adapter never learns about the application it serves.

## Reproduction protocol

1. Check out the baseline at the pinned commit (or the commit under test).
2. Hand the task above plus the baseline to a fresh builder — for an AI agent, the
   baseline's `README.md` navigation protocol is the only other instruction needed.
3. Run `./verify.sh` from this repo against the rebuilt project (it takes the project
   directory as an optional first argument, defaulting to this repo).
4. Compare the rebuild's deviations list against this repo's README. New deviations
   mean the baseline has a gap or an ambiguity — feed them back into the baseline,
   as its maintenance protocol requires.

When the baseline changes materially, re-run this test and update this pin. The pin
is the "known-good baseline state" marker: if a rebuild against a newer baseline
commit fails, the baseline regressed — not this repo.

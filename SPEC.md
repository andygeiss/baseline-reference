# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade application?*

## Baseline pin

Built against baseline commit **`c3d2438`**, the v4.0.0 change: **there is no CI
server.** The gates run from the Makefile — `make check` against the working tree,
`make ci` against the commit — and a CLI's release is a tag, with `go install` as the
one channel. No rule about the code moved.

**What moved here.** `.github/` is gone: the `ci.yml` workflow and the Dependabot
config. The Makefile is the baseline's new canonical one — targets alphabetical,
`.DEFAULT_GOAL = check`, the `ci` target, and `lan` in its alphabetical place. The
module path moved from `/v3` to `/v4` in `go.mod` and every import, because this
repository's tags mirror the baseline's and a v4 tag on a `/v3` module never stamps
(`patterns/go-cli.md`). The README's "never released" waiver narrowed to what is still
true of it: the tags here are the baseline's numbers, so they say nothing about
`gochat`'s contract. The release workflow and its artifacts, which that entry called the
acceptance test's one coverage hole, no longer exist to be uncovered.

**What the run proved.** `GOTOOLCHAIN=go1.26.7 ./verify.sh`: 74 gates, exit 0 — the same
toolchain pin as v3.11.1, for the same reason: the machine's Go is 1.27.0 and the baseline
adopts a major at its first patch. `make ci` ran green on this commit, which is the check
a CI server used to make; its first line names the toolchain, which the runner used to
record.
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

1. `./verify.sh` exits 0 — it runs every mechanical gate from the baseline's
   `operations/ci.md` **plus** a live smoke test of both running binaries
   (health endpoint, CSP header, the invite-code gate and the secret staying out
   of the logs, session cookie flags, token renewal on sign-in, rate limiting,
   plain-form and htmx flows, the poll's 204 and 200 answers, escaping, the 422
   validation answer, machine tokens end to end, CSRF rejection, the backup
   snapshot, state across a restart, graceful shutdown, and the `gochat` client
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

# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade application?*

## The brief

The baseline's `README.md` *The task brief* asks every project to keep these four fields
at its root, job and why one line each; each bullet names its long form.

- **Job** — a person chats from a phone and a program from a terminal, on one server
  (*The task*, below).
- **Why** — for whoever maintains the baseline: a rule that cannot be followed to a
  working application is a rule with a gap, and this repository is where that shows
  (the question this file opens with).
- **Guardrails** — the README's *Baseline deviations* and *Decisions the baseline makes a
  project name*.
- **Done means** — `./verify.sh` exits 0, `make ci` is green, and both checklists walk
  clean (*Acceptance criteria*, below).

## Baseline pin

Built against baseline commit **`7c5c2a4`**, no code before the brief: **every task
carries four fields the user has seen before the first line of code — job, why,
guardrails, done means — and every project keeps `SPEC.md` at its root as the
project-level brief.** The agent drafts, asks only what it cannot infer, and re-reads the
brief as the acceptance test before declaring done; a task brief is a delta against this
file.

**What moved here.** *The brief* above: the four fields at project scale, each bullet
naming its long form, and `make ci` joining *Acceptance criteria* because *Done means*
names it. `verify.sh` gates the file and the four labels as its ninth step, accepting a
bold name or a `Field:` line and rejecting a heading; it was run red on a missing file, a
missing field, and a renamed one before it was trusted. The README's index line names the
brief, and the reproduction protocol sends a builder to `SKILL.md`, the file the
baseline's README tells agents to read.

**What the run proved.** `GOTOOLCHAIN=go1.26.7 ./verify.sh`: 78 gates, exit 0 — one more
than v4.2.0, the brief step. `GOTOOLCHAIN=go1.26.7 make ci` is green on this commit, its
first line `go version go1.26.7 darwin/arm64`. No code moved.

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
5. `make ci` is green: the same gates against `git archive HEAD`, so nothing missing
   from `git add` can pass.

## Reproduction protocol

1. Check out the baseline at the pinned commit (or the commit under test).
2. Hand the task above plus the baseline to a fresh builder — for an AI agent, the
   baseline's `SKILL.md` protocol is the only other instruction needed.
3. Run `./verify.sh` from this repo against the rebuilt project (it takes the project
   directory as an optional first argument, defaulting to this repo).
4. Compare the rebuild's deviations list against this repo's README. New deviations
   mean the baseline has a gap or an ambiguity — feed them back into the baseline,
   as its maintenance protocol requires.

When the baseline changes materially, re-run this test and update this pin. The pin
is the "known-good baseline state" marker: if a rebuild against a newer baseline
commit fails, the baseline regressed — not this repo.

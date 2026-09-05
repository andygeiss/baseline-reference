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

Built against baseline commit **`9035558`**, the two decisions v4.4.0 left open:
**`encoding/json/v2` is the JSON package, and htmx 4.x waits for 4.0.1.**

**What moved here.** Every `encoding/json` import is `encoding/json/v2` — seven files
across `internal/app`, `internal/chatapi`, `internal/anthropic`, and `cmd/gochat`. The
`/api` decoder is one `json.UnmarshalRead` call with `json.RejectUnknownMembers(true)`:
v2 refuses a second object after the first on its own, so the second `Decode` that
checked for trailing content is gone, and the 413 for a body over the cap still comes
out of the same `errors.As` on `*http.MaxBytesError`. The two adapters decode with
`UnmarshalRead` over the same `io.LimitReader`. The refusal test gained two cases the
move makes new: a field in the wrong case, which v1 would have taken, is now a 400; a
body over the cap is a 413. `-json` output keeps its own trailing newline, which
`MarshalWrite` does not write. htmx stays at 2.0.10; nothing in the layout changed.

**What the run proved.** `GOTOOLCHAIN=go1.27.1 ./verify.sh`: 79 gates, exit 0, the same
count as v4.4.0. `GOTOOLCHAIN=go1.27.1 make ci` is green on this commit. `go fix -diff`
found nothing to rewrite: the one `omitempty` here sits on a map, which the `omitzero`
fixer leaves alone.

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

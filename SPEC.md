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

Built against baseline commit **`b2f040a`**, on the baseline's branch `owed-fixes`:
untagged, and not merged. It is the last commit of the 2026-11-10 backlog run that
changes anything a project reads. That run read the baseline's oldest unread documents
four times and did not converge. Its owner then took two areas out of the baseline, a
CLI that listens and a program a tool keeps running, and changed what a tag needs: no
known high, and every known medium listed. **One high is known and waits on his
decision:** a password reset leaves the user's machine tokens. This repository follows
the rule as it is written, so its reset leaves them too.

**What moved here,** in three syncs on the branch `sync-owed-run`. Sessions: the store
hashes its token, keeps a deleted row emptied, writes no emptied and no expired row, and
its janitor runs an hour behind; the user row carries a password counter, and every
statement a password or a reset link allows names the value its request loaded. The 500
is a page that needs no session. An adapter's error carries the status code, never the
URL or what the other side wrote, and no log line names a person. The room map's socket
is counted before the hijack, reads the connection itself, and is closed on `pagehide`;
its recipe checks the `wasm-opt` that `tinygo env WASMOPT` names. Every submit button
inside a fragment carries an id, the headers test compares a literal, an owned file and a
signed-in page are `no-store`, and `gochat` refuses a stray argument and a token with a
control character.

**A fourth sync** follows the baseline's `26f8f08` and `c512e31`, what that run's fourth
pass corrected. `serverError` makes no exception for a client that left: an earlier sync
had written one, and the baseline took it back. An adapter's transport error keeps its
type and none of its text, a context that ended comes back as itself, and an answer that
does not decode is a fixed sentence. A urlencoded post to the upload route keeps its 413
and its 400. The server refuses a stray argument, and `gochat` trims and checks a token
from the environment as it does one from a file. A new token's secret is in the answer to
the request that made it, never in the session. The migration walk reads `UPDATE OR
REPLACE`. And from `b2f040a`, which answers what this sync found: a cancel that lands
while an answer is being read comes back as the context's error, not as an answer that
does not decode.

**One rule is not followed, and not waived:** the baseline now keeps a blob for what fits
its blanket 1 MiB cap, and attachments here are blobs under a 3 MiB cap. **And two rules
do not compose on one request:** making a token answers its POST with the page, where
every plain form answers 303, because the secret is shown in that answer and nowhere
after it. The README records both under *Baseline deviations*; lowering the cap, moving
the bytes to a directory, or a waiver is Andy's to choose, and so is which of the two
rules gives way.

`./verify.sh` exits 0 against `b2f040a` (96 steps).

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
> 9. Every room has a map: a canvas showing everyone who has it open and where
>    each of them is pointing, moving as they move. It is the one page that is a
>    program, built per `patterns/wasm-client.md`; the room works without it.

## Acceptance criteria

1. `./verify.sh` exits 0 — it runs every mechanical gate of the baseline's `check`
   recipe (`stack/makefile.md`, explained in `operations/ci.md`) **plus** a live smoke
   test of both running binaries (health endpoint, CSP header, the invite-code gate
   and the secret staying out of the logs, session cookie flags, token renewal on
   sign-in, rate limiting, plain-form and htmx flows, the poll's 204 and 200 answers,
   escaping, the 422 validation answer, machine tokens end to end, CSRF rejection, the
   backup snapshot, state across a restart, graceful shutdown, the room map's module,
   scripts and socket — Origin, cookie, and the 1001 on shutdown — and the `gochat` client
   talking to all of it).
2. The baseline's `checklists/web-application.md` and `checklists/cli-tool.md`
   both walk clean, with deviations waived in the README.
3. The vendored htmx file matches the version pinned in the baseline's
   `VERSIONS.md` (verify.sh checks its SHA-256).
4. `go list -deps` on each adapter — `./internal/chatapi` and
   `./internal/anthropic` — names `internal/domain` and nothing else of ours: an
   adapter never learns about the application it serves.
5. `make ci` is green, its `go version` line at the baseline's pin or a later patch of
   it: the same gates against `git archive HEAD`, so nothing missing from `git add` can
   pass.

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

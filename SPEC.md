# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade application?*

## Baseline pin

Built against baseline commit
**`551b787`** — tag **v3.3.0**, which added live updates, machine tokens, the
CLI's secret rule, and the rule that a CLI checks its name against the PATH.

The app under test is **Go Chat**: a mobile-first chat application with a
command-line client. v3.2.0 swapped it in for the todo app, which had swapped in
for tic-tac-toe.

**The client is `gochat`, and it was `chat` for a day.** `/usr/sbin/chat` is a
PPP utility on every macOS box, so the tool a person installed was not the tool
that ran. The rename took the environment variables with it — `GOCHAT_ADDR`,
`GOCHAT_TOKEN_FILE`, `GOCHAT_TOKEN` — and the baseline gained a rule and a
checklist box for it.

**Why the product changed.** The todo app's own README named the holes it could
not close: `patterns/go-http-client.md` was "the acceptance test's one real
coverage hole", the adapter half of `patterns/go-ports-adapters.md` was
unexercised for the same reason, and sessions, flash messages, secrets,
backups, bottom navigation, and `<dialog>` were all waived or missing because a
single-page list needs none of them. A chat application needs every one, and it
needs a second binary — so `project-types/cli-tool.md`, `patterns/go-cli.md`,
and `checklists/cli-tool.md` get their first reference implementation too.

**What it exposed before a line of code was written.** The baseline had no rule
for keeping a page current — no polling, no SSE, no WebSockets, nothing. A chat
application cannot be built without answering that, and the answer is
constrained by "htmx is the only script tag". `patterns/htmx-live-updates.md` is
that answer: polling with a server-held cursor, a 204 for the quiet case, and
the reasoning for why SSE is not in this baseline yet.

**Found by building it, fixed in the baseline:**

1. **A `Secure` session cookie cannot be exercised over the HTTP this baseline
   mandates.** `patterns/go-auth-sessions.md` set the flag unconditionally, and
   `project-types/web-application.md` says the binary only ever speaks plain
   HTTP behind a TLS proxy. Together they make an app nobody can sign in to in
   development, and an acceptance test that cannot reach a single authenticated
   route. Two rules that are each correct and do not compose — the class of
   defect only a running application finds.
2. **htmx's 286 poll-stop depends on the `responseHandling` array.** Traced
   through the vendored htmx 2.0.10: the cancel sits inside the swap branch, so
   the canonical `htmx-config` meta works by way of its `[23]..` rule rather
   than by design. Tightening that pattern would leave polls running forever,
   silently.
3. **`patterns/go-ports-adapters.md` had no checklist box in any checklist,**
   and `go-ports-adapters.md` itself was missing from the README's file tree —
   both since the pattern shipped. A pattern the checklists never name is a
   pattern nothing enforces, which is why the todo app could carry the adapter
   half unexercised without anything flagging it.

**Two surfaces, and why.** The server renders HTML for browsers and JSON at
`/api` for programs. These are separate surfaces, not two representations of
one: nothing negotiates on `Accept`. `stack/htmx.md` rule 2 — "server returns
HTML, never JSON" — is a rule about htmx, and a command-line client is not htmx.
`patterns/go-cli.md` requires `-json` for machine consumers, and something has to
produce that JSON.

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
4. `go list -deps ./internal/chatapi` names `internal/domain` and nothing else of
   ours — the adapter never learns about the application it serves.

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

# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade application?*

## Baseline pin

Built against baseline commit
**`b94cbeb`**, whose rule commits carry the shape this release implements: **work a
request starts and does not wait for** (`patterns/go-background-work.md`). The corpus ruled
work on a schedule and request-scoped work, and had nothing for the third shape — a request
starts it, it outlives the request, and it dies with the process.

**The assistant used to answer inside the request**, and this repository recorded that as a
conformance note saying a production chat would do it the other way. It now does:

- **`handleMessagePost` starts the reply and answers at once.** The sender pays nothing for
  the model, and the answer reaches the room on their next poll — the poller that was
  already there.
- **The reply keeps the request's values and drops its cancellation**
  (`context.WithoutCancel`), so somebody who closes the tab still gets an answer.
- **The process stays in charge of when it ends.** `context.AfterFunc(a.stopping, cancel)`
  hangs the reply's cancel off the errgroup's context, and `App.Wait` joins the goroutine
  after `g.Wait()` — `srv.Shutdown` does neither, because the goroutine is not an in-flight
  request.
- **`assistantBudget` has left the timeout ladder.** There is no socket above this work, so
  10s is a backstop for a wedged model rather than a rung under `WriteTimeout`.

**The rule that did not survive contact.** *Tests wait on the counter, never on the clock*
was half an answer, and building it showed which half. Deleting `context.AfterFunc` turns
`TestShutdownEndsAReplyInFlight` red after ten seconds — the shutdown hang measured rather
than argued. Deleting `a.running.Add(1)` turned **nothing** red: an uncounted reply still
lands, every time, on a machine that is not loaded.

The first answer here was to give up and gate the counter at the source. The corpus had a
better one already: `patterns/go-testing.md` mandates `testing/synctest` for goroutine
coordination, and in a bubble `synctest.Wait` returns only once every other goroutine is
durably blocked — so a `Wait` that returned early is visible to a `select` with a
`default`. `TestWaitJoinsAReplyInFlight` is red in 0.04s without the counter, with no clock
and no deadlock. **The rule now says so**, along with the two things that have to stay
outside the bubble, both found by running it: the listener blocks on a socket, which never
counts as durably blocked, and scs's session store holds a ticker that never exits.

**One test outside the process had to change, and the reason is worth recording.** The
`verify.sh` smoke test read the room straight after posting a mention, which was safe only
while the reply was synchronous. `go test` can wait on `App.Wait`; a shell cannot, so that
gate is now a bounded retry — bounded, so a reply that never comes fails it rather than
hanging it. **Detaching work costs every out-of-process test its happens-before edge.**

**What changed here:** two fields and one method on `App`, one function split in two in
`internal/app/messages.go`, the errgroup moved above `app.New` in `cmd/server/main.go`,
three handler tests, the test harness split so an app can be built without its listener,
one source gate and two reworked smoke gates in `verify.sh`.

This repository's [GLOSSARY.md](GLOSSARY.md) is the file that baseline's
`patterns/glossary.md` quotes as its worked example. The two are character
identical, so a change to either is a change to both — the attachment, outbox and
reset-link entries landed in both. `Caddyfile.lan` is the second such file: it is
character-identical to the snippet in `patterns/local-https.md`.

**This repository opts into local HTTPS**, because it is installable and a browser
only offers install over HTTPS — so install is the one feature here a phone cannot
try over a plain LAN address. Six `verify.sh` gates cover five of the six boxes the
web checklist added, and the sixth (the authority never leaving this machine) is
enforced by the one that proves no certificate ever entered the history.

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

1. **A `Secure` session cookie does not survive the HTTP this baseline
   mandates.** `patterns/go-auth-sessions.md` set the flag unconditionally, and
   `project-types/web-application.md` says the binary only ever speaks plain
   HTTP behind a TLS proxy. Two rules that are each correct and do not compose —
   the class of defect only a running application finds. *(Corrected in v3.3.1,
   twice over: the baseline itself was never fixed, only this repository, and
   both tellings overstated the damage. Loopback is a secure context, so `curl`
   and browsers do return the flagged cookie over `http://localhost` — this very
   acceptance run included. LAN addresses, containers, and plain-HTTP staging are
   what break.)*
2. **htmx's 286 poll-stop depends on the `responseHandling` array.** Traced
   through the vendored htmx 2.0.10: the cancel sits inside the swap branch, so
   the canonical `htmx-config` meta works by way of its `[23]..` rule rather
   than by design. *(Corrected in v3.3.1: "tightening that pattern" was the wrong
   warning — `2..` still matches `286`. htmx takes the first match, unanchored,
   so the real traps are replacing `[23]..` with the codes the app returns, and
   grouping `422` after `[45]..`, which silently kills form validation.)*
3. **`patterns/go-ports-adapters.md` had no checklist box in any checklist,**
   and `go-ports-adapters.md` itself was missing from the README's file tree —
   both since the pattern shipped. A pattern the checklists never name is a
   pattern nothing enforces, which is why the todo app could carry the adapter
   half unexercised without anything flagging it.
4. **The session sweeper never ran.** `time.NewTicker` does not fire at zero, so
   a worker on a five-minute interval sweeps nothing in any process that
   restarts more often than that — which is every process under development, and
   every service that deploys more often than it cleans up. The loop looks alive,
   `g.Wait()` holds it open, and the table it was meant to trim grows forever.
   The run-once now lives inside `every`, where both workers get it, and
   `patterns/go-background-work.md` owns the rule (it was in
   `patterns/go-http-server.md` when this was found). *(Found in v3.5.0, by
   building the assistant — nothing in a document review had reached it.)*

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

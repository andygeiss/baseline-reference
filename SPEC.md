# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade application?*

## Baseline pin

Built against baseline commit
**`98885e9`** — tag **v3.8.0**, which added `patterns/go-authorization.md`: the tier-1
rules for *may this actor touch this row*, which no document had. `go-auth-sessions.md`
answered who is signed in; nothing said an owned-row query filters by the session's user,
so a handler that rendered somebody else's row passed every box in every checklist. The
release also taught the baseline's own `make structure` to check the two claims that
repository makes about its shape, after both turned out to be wrong.

**This repository already satisfied most of the new document, which is where it came
from.** `Tokens.Delete` has taken the actor as a parameter and carried `user_id` in the
`WHERE` clause since machine tokens existed, `ByUser` scopes the list, and
`TestRevokingSomebodyElsesTokenDoesNothing` is the two-user test the pattern now requires
of every project.

**Two rules did not survive contact, and this repository is why.** Both were fixed in the
pinned commit before the tag:

1. **The private-by-default rule mandated a mechanism.** It read "registered on a mux
   mounted behind `requireLogin`", which fits one class of private route under one prefix
   and nothing else. This app has three classes on paths that do not nest, and a prefix
   mount loses the collection path outright: with only `/rooms/` registered, `GET /rooms`
   becomes a 307 to `/rooms/`, which the inner mux — holding `GET /rooms` — then 404s. The
   rule now states the invariant and gives both shapes.
2. **"404 for somebody else's row" could not be met by a redirecting mutation.** Token
   revocation answers its own 303 and "that token is already gone", which is the same
   sentence whether the row belonged to somebody else or was revoked in another tab. The
   rule is now that the two answers match; the status code follows the route.

**What changed here:** `internal/app/routes.go` moved from wrapping each handler to one
route table whose access class is a required positional field, `guard` panics at boot on a
class it does not know, and `internal/app/routes_test.go` walks the table to prove every
non-public route turns an anonymous request away. `README.md` gained *Who owns what*,
which the new document requires: `tokens` and `users` have owners, `rooms` and `messages`
are shared on purpose.

This repository's [GLOSSARY.md](GLOSSARY.md) is the file that baseline's
`patterns/glossary.md` quotes as its worked example. The two are character
identical, so a change to either is a change to both. `Caddyfile.lan` is the second
such file: it is character-identical to the snippet in `patterns/local-https.md`.

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

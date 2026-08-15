# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade web application?*

## Baseline pin

Built against baseline commit
**`baf691d`** — tag **v3.1.0**, the governance release of 2026-08-15: the tag gate,
the rule tiers, and the 90-day staleness switch.

The app under test is a mobile-first todo app (v2.0.0 swapped it in for
tic-tac-toe). It is small on purpose, and it exercises more of the baseline than
the game did: the add form is the first real user of
`patterns/go-forms-validation.md` here, so the 422 answer, the kept value, and
`HX-Push-Url: false` on a boosted failure are all under gate now.

v2.0.1 fixed seven defects across eight documents. Three of them reach this repo,
and this sync applies all three: the failing field now carries `aria-invalid` next
to its `aria-describedby`, the task list carries `role="list"` because the CSS
drops its markers, and `version()` reads `info.Main.Version` — the canonical
reader in `patterns/go-cli.md` — instead of hand-assembling `vcs.revision`. The
sweep's remaining fixes are about rules this app does not use (Caddy,
bottom navigation, the two-pool snippet this repo already had right) or about
wording, which `DESIGN.md` picked up.

v2.1.0 added `patterns/go-ports-adapters.md` and changed nothing here: the
`TaskStore` port in `internal/app/app.go` was already consumer-defined, in domain
words, at four methods. The pattern's real subject — an adapter for *somebody
else's* system — is unexercised for the same reason `go-http-client.md` is, and
the README records that hole.

v3.0.0 moved servers, runbooks, and the container templates into
[baseline-ops](https://github.com/andygeiss/baseline-ops), leaving the baseline
with the deployment *contract* alone. This sync applies the split to this
repository: the binary's side of that contract stays and stays gated, and the
deployment's side is gone — `Dockerfile`, `.dockerignore`, and the
`release.yml` that pushed an image to GHCR and deployed it over SSH. That
workflow contradicted the operations repository it was meant to serve, which
sanctions no registry and no CD pipeline for a web application; it named deploy
secrets this repository does not have; and it targeted a server this repository
is never on. The templates it duplicated are verified where they are owned:
baseline-ops builds its own `templates/Dockerfile` against a checkout of this
repository.

The same release rewrote ten pattern documents to stop naming one runtime, and
two of those reach the code. `cmd/server/config.go` no longer says the unit file
sets `ENV`; the deployment does. And `go-sqlite.md` §Backups became a question
with three legitimate answers rather than a choice of two mechanisms, so this
app's throwaway data is now the first row rather than a waiver — the README
records the decision, which is what that row asks for. Everything else in the
ten was wording this repository never copied: `cfg.Env` already picked the log
handler, `LogLevel` was already a `slog.Level`, and the `host` flag already
called the proxy the public listener.

**Found by this sync, fixed here and fed back:** §Version stamping promised
`info.Main.Version` is the tag when HEAD sits on one, and past v1 that holds only
if the module path carries the major version. It did not: on its first v3 tag,
this repository built a binary reporting
`v1.17.1-0.20260815164832-b67cd862f0fb`. The
module path is now `github.com/andygeiss/baseline-reference/v3`, the stamp is the
tag again, and the rule became **baseline v3.0.1**. The reproduction protocol
working end to end: the acceptance test hit a promise the baseline could not
keep, and the baseline changed.

**On the tag itself.** This repository's tag mirrors the baseline version it was
built against, so the sync that produced baseline v3.0.1 is released here as
v3.0.1 — not v3.0.0, which was cut before the stamp was fixed and has been
withdrawn. Rewriting a published tag is only acceptable because nothing imports
this repository and the tags were hours old; a library would have carried the bad
release forever and added a patch on top instead.

v3.1.0 changed no rule this app implements — it added the release gate, the rule
tiers, and a 90-day staleness switch — so no Go, CSS, or template line moved in
this sync. What did move is the README's deviations section. The baseline now
requires six fields on a waived rule (rule, document, date, decider, why, what
contains it), and the five real waivers here carried everything but the date and
the decider; those came out of `git log -S`. Three bullets that were never
waivers — no backups, no outbound HTTP, the partial type scale — now say so
plainly, because a reader hunting for gaps counts every bullet in that section.

**Found by this sync, fixed in the baseline before the tag:** the waiver format
first mandated the heading `## Waived baseline rules`. This repository's section
holds waivers and conformance notes together, so that heading would have
relabelled "this app needs no backups" as a waived rule. The baseline now
requires the fields and lets the heading fit the list. The first release under
the new gate found a defect in that release — which is the gate working, one
sync earlier than the two runs that shipped without it.

When the baseline changes materially, re-run this test (see protocol below) and update
this pin. The pin is the "known-good baseline state" marker: if a rebuild against a
newer baseline commit fails, the baseline regressed — not this repo.

## The task (give this to the builder, human or AI, verbatim)

> Build a mobile-first todo app — a simple way to organize tasks — by following
> `project-types/web-application.md` of the engineering baseline. Use only what the
> baseline mandates or approves; record every deviation in the README as the baseline
> requires.
>
> Functional requirements:
> 1. One page shows the list: a form to add a task, then the tasks themselves, open
>    ones first.
> 2. A task can be added, ticked off, ticked back on, and deleted.
> 3. A title is trimmed and capped; an empty or oversized one is refused with the
>    reason next to the field and the typed value kept.
> 4. Acting on a task that is already gone heals the list instead of failing.
> 5. Every interaction works with htmx disabled (plain forms, full-page renders).
> 6. The list survives a server restart.

## Acceptance criteria

1. `./verify.sh` exits 0 — it runs every mechanical gate from the baseline's
   `operations/ci.md` **plus** a live smoke test of the running binary
   (health endpoint, CSP header, plain-form flow, htmx fragment flow, the 422
   validation answer, ticking a task off and deleting it, CSRF rejection,
   state across a restart, graceful shutdown).
2. The baseline's `checklists/web-application.md` walks clean, with deviations
   waived in the README.
3. The vendored htmx file matches the version pinned in the baseline's
   `VERSIONS.md` (verify.sh checks its SHA-256).

## Reproduction protocol

1. Check out the baseline at the pinned commit (or the commit under test).
2. Hand the task above plus the baseline to a fresh builder — for an AI agent, the
   baseline's `README.md` navigation protocol is the only other instruction needed.
3. Run `./verify.sh` from this repo against the rebuilt project (it takes the project
   directory as an optional first argument, defaulting to this repo).
4. Compare the rebuild's deviations list against this repo's README. New deviations
   mean the baseline has a gap or an ambiguity — feed them back into the baseline,
   as its maintenance protocol requires.

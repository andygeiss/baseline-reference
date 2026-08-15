# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade web application?*

## Baseline pin

Built against baseline commit
**`8c492e8`** — tag **v2.0.1**, the full-corpus sweep of 2026-08-15.

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

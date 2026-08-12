# Specification: baseline-reference

This repository is the **reproducible acceptance test** of the
[engineering baseline](https://github.com/andygeiss/baseline). It exists to answer one
question: *does following the baseline, and nothing but the baseline, produce a
working, production-grade web application?*

## Baseline pin

Built against baseline **v1.7.1** — commit
**`b5160a17d63f336962e5ed046697c4cd07df7e93`** (2026-08-11).

When the baseline changes materially, re-run this test (see protocol below) and update
this pin. The pin is the "known-good baseline state" marker: if a rebuild against a
newer baseline commit fails, the baseline regressed — not this repo.

## The task (give this to the builder, human or AI, verbatim)

> Build a two-player hot-seat tic-tac-toe web application by following
> `project-types/web-application.md` of the engineering baseline. Use only what the
> baseline mandates or approves; record every deviation in the README as the baseline
> requires.
>
> Functional requirements:
> 1. The home page offers "start a new game". A new game gets an unguessable ID and
>    its own URL (`/games/{id}`).
> 2. Players alternate placing X and O by clicking board cells; X goes first.
> 3. The game detects all eight winning lines and the draw; finished boards accept no
>    further moves.
> 4. Every interaction works with htmx disabled (plain forms, full-page renders).
> 5. Game state survives a server restart.

## Acceptance criteria

1. `./verify.sh` exits 0 — it runs every mechanical gate from the baseline's
   `operations/ci.md` **plus** a live smoke test of the running binary
   (health endpoint, CSP header, plain-form flow, htmx fragment flow, CSRF
   rejection, graceful shutdown).
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

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

Built against baseline commit **`b6a731e`**, the Go 1.27 defect backlog:
**thirty-one corrections to rules the baseline already shipped, six of them tier 1.**

**What moved here.** The three secrets in `Config` — `InviteCode`, `AnthropicKey`,
`SMTPPassword` — are a `Secret` type carrying `LogValue`, `String` and `MarshalText`, and
the three call sites that hand one to an adapter now write the `string(...)` conversion
that is the only way past it. `Config.LogValue` stays: it is the allowlist, and the type is
what holds where `LogValue` does not fire. `newTestDB` asserts both pools are idle at the
end, registered after the `Close` cleanup so LIFO runs it first — the store was already
closing every `Rows`, `Stmt` and `Tx`, so this locks in a practice the baseline had never
written down. `make fmt` loops `go fix` to a clean `-diff` and runs `goimports` again after
it. `apiJSON`'s buffering comment now gives the v2 reason rather than the v1 one: `Marshal`
returns the bytes it got through *and* an error. `parseBaseURL` lost the
`strings.TrimSuffix` whose comment claimed `JoinPath` would double the slash — it does not.

**What the run proved.** `TestConfig_SecretsNeverLogged` is seven cases, and six of them
fail without the `Secret` type: logging a struct that *contains* the config, a slice of
configs, a map of configs, `%+v` over the config, and the field on its own by either route
all print `SUPER-SECRET-KEY` when the field is a plain `string`. That is the whole of the
baseline's tier-1 claim, checked rather than argued. The new `newTestDB` assertion passes
across the suite, so nothing here leaks a connection. `./verify.sh`: all gates, exit 0.
`make fmt` converged in one run and added the `fmt` import the new test needed.

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

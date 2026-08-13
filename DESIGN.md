---
version: alpha
name: baseline-reference
description: A two-player hot-seat tic-tac-toe web application.
colors:
  bg: "oklch(99% 0.002 260)"
  surface: "oklch(96% 0.005 260)"
  text: "oklch(22% 0.02 260)"
  text-muted: "oklch(45% 0.02 260)"
  accent: "oklch(45% 0.17 260)"
  error: "oklch(45% 0.17 25)"
  border: "oklch(62% 0.02 260)"
  bg-dark: "oklch(18% 0.01 260)"
  surface-dark: "oklch(24% 0.01 260)"
  text-dark: "oklch(93% 0.01 260)"
  text-muted-dark: "oklch(72% 0.02 260)"
  accent-dark: "oklch(75% 0.12 260)"
  error-dark: "oklch(75% 0.12 25)"
  border-dark: "oklch(56% 0.02 260)"
  primary: "oklch(45% 0.17 260)"
  primary-dark: "oklch(75% 0.12 260)"
typography:
  body:
    fontFamily: "system-ui, sans-serif"
  mono:
    fontFamily: "ui-monospace, monospace"
rounded:
  radius: "0.375rem"
components:
  button:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.bg}"
    rounded: "{rounded.radius}"
---

# Design: baseline-reference

## Overview

This file is the project's design system: the theme values, contrast
floors, and component inventory every page is styled from. It is written
for whoever styles a page — usually an AI coding agent — and design tools
parse the same file. The theme is the baseline's neutral starting set,
unchanged: near-white surfaces, one accent hue for everything
interactive, generous whitespace. The dark scheme follows the OS setting —
there is no toggle. Every CSS value in this file is identical,
character for character, to the same value in `web/static/css/app.css`; the
two files change in the same commit.

## Colors

Every color `app.css` writes comes from the roles above — components never
use a raw color value. `accent` colors everything interactive: links, the
primary button, focus rings. `primary` restates the `accent` values
character for character: the spec requires a `primary` palette, and tools
invent one when it is missing. `surface` is the resting fill of buttons and
board cells. `error` is reserved for validation failures; the game
currently renders none — a rule-breaking move comes back as `text-muted`
advice, not an error. Hover feedback swaps roles at the use site — a
hovered cell trades `surface` for `bg` — never extra shade tokens.

Measured contrast (2026-08-12): every text role on both backgrounds ≥ 6.6:1
in both schemes; `border` on both backgrounds ≥ 3.2:1; the primary button
≥ 7.4:1. After any color change, re-measure and update these numbers.

## Typography

The system font stack, no web fonts: `body` for text, `mono` for code. There
is no fixed size scale — sizes are fluid `clamp()` expressions at the use
site, so type tracks the viewport and the user's font-size setting.

## Layout

One fluid spacing value drives all whitespace: `clamp(1rem, 0.5rem + 2vw, 2rem)`,
with quarter, half, and double steps derived from it. The content column
measures `30rem` — game UI, not prose — and card and sidebar pages cap at
`80rem`. Layout is mobile-first: the base styles are the 320 px layout, and
wider screens only add columns.

## Elevation & Depth

No shadows: this is the minimal surface style. Depth is one step deep:
`surface` panels on the `bg` page ground, separated by `border` lines. A
design needing a taller stack than that gets redesigned flatter.

## Shapes

One radius, `0.375rem`, on every rounded box: buttons and board cells.
Pills and circles are not part of this system.

## Components

Every component composes the roles above, and every interactive state
(hover, focus-visible, active, disabled, loading) is styled:

- **Button** — primary actions use the `button` composite above: `accent`
  background, `bg` text. Secondary actions are plain buttons: `surface`
  fill inside a `border` boundary.
- **Board** — nine square cell buttons in a three-column grid. A taken or
  finished cell is disabled but keeps full-contrast text — the mark is
  content, not a control state. While a move is in flight the grid dims,
  hidden until the request runs past 100 ms.
- **Status line** — one line above the board announcing the turn or the
  result, with rule-breaking moves explained in `text-muted`.

## Do's and Don'ts

- Do take every color from the roles above. Don't write a literal color
  value outside the stylesheet's tokens layer.
- Do let the OS pick the scheme. Don't add a theme toggle.
- Do use motion as feedback: `150ms` for state changes, `300ms` for
  movement. Don't decorate with motion.
- Do change this file and `app.css` in the same commit. Don't let them
  drift.

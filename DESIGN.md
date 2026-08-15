---
version: alpha
name: baseline-reference
description: A mobile-first todo app — a simple way to organize tasks.
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
task rows. `error` colors one thing: the message under a field the reader
must fix. Acting on a task that is already gone is not a validation failure
— that comes back as `text-muted` advice beside the count. Hover feedback
swaps roles at the use site — a hovered button trades `surface` for `bg`.
Both row controls sit on the row's `surface`, so the same swap works on
them without a second boundary inside the card. The primary button's fill hides
that ground, so it mixes toward `text` with `color-mix()` instead. Neither
adds a shade token.

The manifest's `background_color`/`theme_color` and the two `theme-color`
metas are `bg` converted to sRGB (`#fbfcfd` light, `#0f1216` dark) — those
formats cannot read `oklch()` — and they change with `bg` in the same commit.
The app mark (`favicon.svg` and the four install icons exported from it) is a
white check mark on a rounded square. It carries its own blue,
`oklch(55% 0.18 255)` = `#026fd7`: brighter than
`accent`, which is dark enough to pass as text. The mark does not follow the
theme; it changes only when the mark is redrawn. The maskable and Apple icons
are padded with light `bg` (`#fbfcfd`): both platforms need an opaque square.

Measured contrast (2026-08-13): every text role on both backgrounds ≥ 6.6:1
in both schemes; `border` on both backgrounds ≥ 3.2:1; the primary button
≥ 7.4:1 at rest and ≥ 8.5:1 hovered — its mix moves the fill away from the
label, never toward it. A plain button hovers to `bg`, already one of the
two measured backgrounds. After any color change, re-measure and update
these numbers.

## Typography

The system font stack, no web fonts: `body` for text, `mono` for code. There
is no size ladder. Body copy keeps the size the reader's browser is set to,
and the one heading level the app renders sizes itself fluidly:
`clamp(1.75rem, 1.2rem + 2.4vw, 2.5rem)`. Every fluid size keeps a `rem`
term, so type still grows when a reader raises that default (WCAG 1.4.4).

## Layout

One fluid spacing value drives all whitespace: `clamp(1rem, 0.5rem + 2vw, 2rem)`,
with quarter, half, and double steps derived from it. The content column
measures `30rem` — app UI, not prose — and card and sidebar pages cap at
`80rem`. Layout is mobile-first: the base styles are the 320 px layout, and
wider screens only add columns. Every control a thumb aims at — the field,
the Add button, both row controls — is at least `3rem` (48 px) tall, above
the 44 px floor WCAG 2.5.5 sets.

## Elevation & Depth

No shadows: this is the minimal surface style. Depth is one step deep:
`surface` panels on the `bg` page ground, separated by `border` lines. A
design needing a taller stack than that gets redesigned flatter.

## Shapes

One radius, `0.375rem`, on every rounded box: buttons, the text field, and
task rows. Pills and circles are not part of this system.

## Components

Every component composes the roles above, and every interactive state
(hover, focus-visible, active, disabled, loading) is styled:

- **Button** — primary actions use the `button` composite above: `accent`
  background, `bg` text. Secondary actions are plain buttons: `surface`
  fill inside a `border` boundary. Every button answers a hover, sinks
  `1px` while pressed, and shows an `accent` focus ring.
- **Text field** — the one input in the app. `bg` fill inside a `border`
  boundary, one radius, `3rem` tall, and the same `accent` focus ring every
  other control shows. It takes `font: inherit`, because a UA control
  otherwise picks a small system face of its own.
- **Field error** — one line in `error`, directly under the field it names,
  and the field points at it with `aria-describedby`. It appears only on a
  422 answer, next to the value the reader typed.
- **Task row** — a `surface` card inside a `border` boundary, holding two
  controls: the row itself toggles the task, and a trash button at its end
  deletes it. Both are `3rem` tall and sit on the card's own ground, so
  neither draws a second boundary inside it. A done row keeps the check
  mark *and* strikes its title through in `text-muted`: state carried by an
  icon alone would have no text equivalent. While a change is in flight the
  list dims, hidden until the request runs past 100 ms.
- **Status line** — one line above the list counting what is still open,
  with a stale action ("That task is gone.") explained in `text-muted`.
- **Icon** — a CSS mask, `1em` square, painted with the surrounding text
  color, so it needs no color and no size of its own. Four shapes ship on
  Lucide's 24-unit grid with a 2-unit round-capped stroke: plus (Add),
  circle and check (a row's two states), and trash (delete). The first
  decorates a labeled control; the rest sit in controls that carry their own
  accessible name.

## Do's and Don'ts

- Do take every color from the roles above. Don't write a literal color
  value outside the stylesheet's tokens layer.
- Do let the OS pick the scheme. Don't add a theme toggle.
- Do use motion as feedback: `150ms` for state changes, `300ms` for
  movement. Don't decorate with motion.
- Do size type and icons in `rem` and `em`. Don't add a size-token ladder,
  and don't set a root `font-size` — both override the reader's choice.
- Do change this file and `app.css` in the same commit. Don't let them
  drift.

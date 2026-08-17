---
version: alpha
name: baseline-reference
description: Go Chat — a mobile-first chat app, with a command-line client.
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
primary button, focus rings, and the section the reader is in. `primary`
restates the `accent` values character for character — a file without a
`primary` palette makes a design tool generate a color of its own, so this
theme names it instead. The spec does not require it; writing it down is how
the theme keeps the decision. `surface` is the resting fill of buttons,
message cards, room rows, and the bottom bar. `error` colors one thing: the
message under a field the reader must fix. Hover feedback swaps roles at the
use site — a hovered button trades `surface` for `bg`. The primary button's
fill hides that ground, so it mixes toward `text` with `color-mix()` instead.
Neither adds a shade token.

The manifest's `background_color`/`theme_color` and the two `theme-color`
metas are `bg` converted to sRGB (`#fbfcfd` light, `#0f1216` dark) — those
formats cannot read `oklch()` — and they change with `bg` in the same commit.
The app mark (`favicon.svg` and the four install icons exported from it) is a
white speech bubble, three dots inside it, on a rounded square. It carries its
own blue, `oklch(55% 0.18 255)` = `#026fd7`: brighter than
`accent`, which is dark enough to pass as text. The mark does not follow the
theme; it changes only when the mark is redrawn. The PNGs are rasterized from
`favicon.svg` with `rsvg-convert`, which cannot read `oklch()`, so the raster
source restates that one blue as its sRGB hex. The maskable and Apple icons
are padded with light `bg` (`#fbfcfd`): both platforms need an opaque square.

Measured contrast (2026-08-13): every text role on both backgrounds ≥ 6.6:1
in both schemes; `border` on both backgrounds ≥ 3.2:1; the primary button
≥ 7.4:1 at rest and ≥ 8.5:1 hovered — its mix moves the fill away from the
label, never toward it. A plain button hovers to `bg`, already one of the
two measured backgrounds. After any color change, re-measure and update
these numbers.

## Typography

The system font stack, no web fonts: `body` for text, `mono` for the token
secret and the header names in the help text. There is no size ladder. Body
copy keeps the size the reader's browser is set to, and the two heading levels
the app renders size themselves fluidly:
`clamp(1.75rem, 1.2rem + 2.4vw, 2.5rem)` and
`clamp(1.375rem, 1.1rem + 1.2vw, 1.875rem)`. Every fluid size keeps a `rem`
term, so type still grows when a reader raises that default (WCAG 1.4.4).
Three relative steps sit under them, each in `em` so it compounds correctly
where it is nested: `0.875em` for a timestamp or a "last used" cell,
`0.9375em` for code, because mono faces run large, and `0.875rem` for the
words in the bottom bar. Table cells take `tabular-nums`, so a column of
times lines up.

## Layout

One fluid spacing value drives all whitespace: `clamp(1rem, 0.5rem + 2vw, 2rem)`,
with quarter, half, and double steps derived from it. The content column
measures `30rem` — app UI, not prose — and card and sidebar pages cap at
`80rem`. Layout is mobile-first: the base styles are the 320 px layout, and
wider screens only add columns. Every control a thumb aims at — the fields,
the buttons, a room row — is at least `3rem` (48 px) tall, above
the 44 px floor WCAG 2.5.5 sets. The one exception is the revoke button inside
a table row, at `2.75rem` (44 px): the floor exactly, because the row it sits
in is dense by nature.

**The bottom bar** is where a signed-in reader navigates from: two
destinations, Rooms and You, each an icon over its word, each target
`3.5rem` (56 px). It is `position: fixed`, so it takes no space in the flow —
which means the footer holding it drops its padding, and the page reserves
`--bar` plus the phone's `env(safe-area-inset-bottom)` under its last row.
Signed-out pages have nowhere to navigate, so they have no bar.

## Elevation & Depth

No shadows: this is the minimal surface style. Depth is one step deep:
`surface` panels on the `bg` page ground, separated by `border` lines. The
dialog is the one thing that floats, and the browser's own backdrop is what
separates it. A design needing a taller stack than that gets redesigned flatter.

## Shapes

One radius, `0.375rem`, on every rounded box: buttons, fields, message cards,
room rows, and the dialog. Pills and circles are not part of this system.

## Components

Every component composes the roles above, and every interactive state
(hover, focus-visible, active, disabled, loading) is styled:

- **Button** — primary actions use the `button` composite above: `accent`
  background, `bg` text. Secondary actions are plain buttons: `surface`
  fill inside a `border` boundary. Every button answers a hover, sinks
  `1px` while pressed, and shows an `accent` focus ring.
- **Field** — text inputs and the message box. `bg` fill inside a `border`
  boundary, one radius, `3rem` tall, and the same `accent` focus ring every
  other control shows. Each takes `font: inherit`, because a UA control
  otherwise picks a small system face of its own. The message box is the one
  field a reader may resize, and only vertically.
- **Field error** — one line in `error`, directly under the field it names.
  Adjacent placement is for the eye; the field also points at the message
  with `aria-describedby` and marks itself `aria-invalid`, so a screen reader
  gets both the reason and which field failed. All three appear only on a
  422 answer, next to the value the reader typed. A failure belonging to no
  single field — "We do not know that name and password." — sits at the top of
  the form instead, because naming a field there would say which half was wrong.
- **Flash** — one line in a `surface` well above the page, shown once after a
  redirect ("Room created."), then gone. It is how a plain form reports what
  happened, since a redirect carries no words of its own.
- **Room row** — a `surface` card inside a `border` boundary, the whole row a
  link at least `3rem` tall, a hash icon before the name. The rows sit in a
  `ul` with its markers dropped, so the markup carries `role="list"` — Safari
  drops the semantics along with the marker.
- **Message** — a `surface` card holding two lines: who and when in
  `text-muted`, then what they said. The body keeps its line breaks
  (`white-space: pre-wrap`), because line breaks are how people write lists
  and paste code, and the server keeps them too. The list carries `role="list"`
  for the same reason the room list does, and its last row is the poller — a
  hidden, empty `li` that carries the cursor and nothing else. The time is a
  `time` element: what it shows is relative ("3 minutes ago"), what its
  `datetime` and `title` carry is the exact moment.
- **Show older** — the first row of a long room, and the far end of the list
  from the poller: one centred `accent` link that loads the page before this
  one. It is an `li` because it lives inside the list it extends, and it is a
  real link as well as an htmx trigger, so it works with htmx switched off. It
  is simply absent once there is nothing older, rather than present and
  disabled.
- **Attachment** — a `figure` under the message it belongs to. A picture is
  bounded by the message rather than by its own pixels (`max-inline-size:
  100%`), rounded to `radius`, and links to itself. Anything the app does not
  render — a PDF, a log — is a plain link instead, never a broken image. The
  caption carries the file's name in `text-muted` and, for whoever uploaded it,
  a `2.75rem` remove button with the trash icon.
- **Dialog** — the new-room form, opened by an invoker button with no script
  at all. It fades and rises `0.75rem` on entry, and only on entry: the
  selector is `dialog:modal`, which a server-rendered open dialog never
  matches, so nothing animates on page load. Exit is instant by design.
  `/rooms/new` is the same form as a page, for a browser that does not know
  invoker commands.
- **Token table** — the machine tokens, one row each: label, when it was last
  used, and a revoke button. The new token's secret appears once above it, in
  `mono` inside an `accent` boundary, selectable in one tap.
- **Icon** — a CSS mask, `1em` square, painted with the surrounding text
  color, so it needs no color and no size of its own. Five shapes ship on
  Lucide's 24-unit grid with a 2-unit round-capped stroke: plus (new room, new
  token), hash (a room), user (You), send (post a message), and trash
  (revoke). Each either decorates a labeled control or sits in one carrying
  its own accessible name.

## Do's and Don'ts

- Do take every color from the roles above. Don't write a literal color
  value outside the stylesheet's tokens layer.
- Do let the OS pick the scheme. Don't add a theme toggle.
- Do use motion as feedback: `150ms` for state changes, `300ms` for
  movement. Don't decorate with motion.
- Do size type and icons in `rem` and `em`. Don't add a size-token ladder,
  and don't set a root `font-size` — both override the reader's choice.
- Do keep the bottom bar to destinations. Don't put an action in it — "new
  room" is a button on the page, not a place to go.
- Do change this file and `app.css` in the same commit. Don't let them
  drift.

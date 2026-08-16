# Glossary: Go Chat

The words this project uses. One word per concept; where people say it more
than one way, the runners-up sit under _Avoid_. Wherever a concept shows up —
in the code, on screen, in a URL — it shows up under the word listed here.

**Author** — the user a message is credited to. _Avoid: poster._

**Invite code** — the shared secret somebody must type to make an account, when
the deployment sets one. _Avoid: access code, signup code._

**Message** — one thing somebody said in a room. Never edited, never deleted.
_Avoid: post._

**Room** — one conversation. Everything posted lives in exactly one room.
_Avoid: channel, thread._

**Seq** — the number that identifies a message and fixes its place in the room.
It only grows, so it is also the cursor a reader polls from.
_Avoid: message id, offset._

**Slug** — the ASCII address a room lives at, `/rooms/<slug>`. A room's name is
what people read; its slug is where the room is.

**Token label** — the note a person writes on a machine token ("laptop", "build
server") to tell their tokens apart later. _Avoid: token name, token
description._

# Glossary: Go Chat

The words this project uses. One word per concept; where people say it more
than one way, the runners-up sit under _Avoid_. Wherever a concept shows up —
in the code, on screen, in a URL — it shows up under the word listed here.

**Attachment** — the one file a message may carry. It is stored under a
generated id and the type its bytes turn out to be, never the name or the type
the browser claimed. _Avoid: upload, file, media._

**Author** — the user a message is credited to. _Avoid: poster._

**Invite code** — the shared secret somebody must type to make an account, when
the deployment sets one. _Avoid: access code, signup code._

**Message** — one thing somebody said in a room. Never edited, never deleted.
_Avoid: post._

**Outbox** — the table a message waiting to be emailed sits in. A handler writes
to it and one background sender drains it, so nothing is ever sent inside a
request. _Avoid: mail queue, job queue._

**Reset link** — the one-hour, single-use address that lets somebody set a new
password. Only the SHA-256 of its token is stored, so the link itself exists in
one place: the message that carried it. _Avoid: recovery link, magic link._

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

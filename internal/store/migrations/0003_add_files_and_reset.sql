-- An address is optional: the app never needs one, and only password recovery
-- does. Empty means this account cannot be recovered, which is a choice the
-- person makes on their profile page.
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';

-- When the password last changed. A session records the value it signed in
-- under, so a reset invalidates every session opened before it — including the
-- one the thief is holding. Empty for accounts that predate this column, which
-- compares equal to the empty value their sessions carry.
ALTER TABLE users ADD COLUMN password_changed_at TEXT NOT NULL DEFAULT '';

-- The bytes live here rather than in a directory beside the database. That is
-- the whole reason there is no sweeper in this app: the row and its file are
-- one row, so they cannot disagree, and the snapshot that already protects the
-- messages protects their attachments too (patterns/go-file-uploads.md).
CREATE TABLE attachments (
    id          TEXT NOT NULL PRIMARY KEY,
    message_seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
    -- Who uploaded it. Reading an attachment is shared with everyone who can
    -- read the room; deleting one is not, and this column is the predicate.
    uploader_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The name the browser sent, kept for the download header and nothing else.
    -- It never reaches a filesystem: id is what identifies these bytes.
    name        TEXT NOT NULL,
    -- The type decided by sniffing the bytes, never the one the browser
    -- claimed. This string is what the download sends back.
    kind        TEXT NOT NULL,
    size        INTEGER NOT NULL,
    bytes       BLOB NOT NULL,
    created_at  TEXT NOT NULL
);

-- One attachment per message, so the join that reads a room stays a join.
CREATE UNIQUE INDEX attachments_message ON attachments (message_seq);

-- Only the SHA-256 of the token, never the token: the plaintext exists once, in
-- the message that was sent, and nowhere else (patterns/go-auth-sessions.md).
CREATE TABLE resets (
    hash       TEXT NOT NULL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX resets_expiry ON resets (expires_at);

-- The outbox is written in the same transaction as the thing that caused the
-- mail, and a ticker drains it. A handler that sent inline would hold a request
-- open on somebody else's SMTP server (patterns/go-email.md).
CREATE TABLE outbox (
    id         TEXT NOT NULL PRIMARY KEY,
    recipient  TEXT NOT NULL,
    subject    TEXT NOT NULL,
    body       TEXT NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    sent_at    TEXT
);

-- The sender reads exactly this: what has not gone yet and has attempts left.
CREATE INDEX outbox_unsent ON outbox (sent_at, attempts);

CREATE TABLE users (
    id            TEXT NOT NULL PRIMARY KEY,
    name          TEXT NOT NULL,
    -- name_key is the lowercased name, and it is what UNIQUE guards: two people
    -- may not differ by capitals alone. The folding happens in Go, because
    -- SQLite's NOCASE collation folds ASCII only — it would let "Zoë" and "zoë"
    -- both exist.
    name_key      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL
);

CREATE TABLE rooms (
    id         TEXT NOT NULL PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,   -- what the URL carries
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE messages (
    -- seq is the message's identity and the cursor a reader polls with.
    -- AUTOINCREMENT never hands a number back out, so a deleted message cannot
    -- make a later one repeat a cursor a reader has already passed.
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    room_id    TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    author_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- Every read is "this room, after this cursor", which is exactly this index.
CREATE INDEX messages_room_seq ON messages (room_id, seq);

CREATE TABLE sessions (
    token  TEXT NOT NULL PRIMARY KEY,
    data   BLOB NOT NULL,
    -- Unix seconds. Find compares against it on every request, and the janitor
    -- deletes by it.
    expiry INTEGER NOT NULL
);

CREATE INDEX sessions_expiry ON sessions (expiry);

CREATE TABLE tokens (
    id           TEXT NOT NULL PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The SHA-256 of the secret, never the secret. A leaked database leaks
    -- nothing usable.
    hash         TEXT NOT NULL UNIQUE,
    label        TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT
);

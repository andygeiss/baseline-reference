CREATE TABLE tasks (
    -- seq is the insert order the list is sorted by. AUTOINCREMENT never hands
    -- a number back out, so deleting the last task cannot reorder the next one.
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    id         TEXT NOT NULL UNIQUE,      -- the ID the URLs carry
    title      TEXT NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

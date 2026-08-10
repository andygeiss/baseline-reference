CREATE TABLE games (
    id         TEXT PRIMARY KEY,
    board      TEXT NOT NULL,              -- 9 chars, '.' = empty, e.g. 'X.O...X..'
    next       TEXT NOT NULL,
    status     TEXT NOT NULL,
    winner     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

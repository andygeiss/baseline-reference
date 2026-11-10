-- A counter in place of a time (patterns/go-auth-sessions.md rule 1). A session
-- records the value it signed in under, and every statement that writes a
-- password hash moves it, so a reset or a change ends the sessions opened
-- before it. The time it replaces was stored in whole seconds: two writes
-- inside one second left the stamp where it was, and the session it should
-- have ended signed in.
--
-- users is referenced by five tables, so it is changed in place and never
-- rebuilt: a migration that dropped it would take every message, token and
-- reset with it, at a green boot (patterns/go-data-deletion.md rule 6). ADD
-- COLUMN and DROP COLUMN touch no row.
ALTER TABLE users ADD COLUMN password_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users DROP COLUMN password_changed_at;

-- An emptied session is a row whose data is NULL: the mark a logout leaves, so
-- that a request still in flight cannot commit the session back
-- (patterns/go-auth-sessions.md). The column was NOT NULL and the mark an empty
-- blob; SQLite cannot drop a NOT NULL in place, so the table is made again.
--
-- No table references sessions, which is what lets this one be dropped, and its
-- rows are not copied: every session in it recorded a time, and there is no
-- counter to read out of one. Everybody signs in once more after this runs.
DROP TABLE sessions;

CREATE TABLE sessions (
    token  TEXT NOT NULL PRIMARY KEY,
    -- NULL once the session is deleted. See internal/store/sessions.go.
    data   BLOB,
    -- Unix seconds. Find and Commit compare against it on every request, and
    -- the janitor deletes by it an hour late.
    expiry INTEGER NOT NULL
);

CREATE INDEX sessions_expiry ON sessions (expiry);

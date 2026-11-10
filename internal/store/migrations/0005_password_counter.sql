-- A counter in place of a time (patterns/go-auth-sessions.md rule 1). A session
-- records the value it signed in under, and every statement that sets a new
-- password moves it, so a reset ends the sessions opened before it. The time
-- it replaces was stored in whole seconds: two writes inside one second left
-- the stamp where it was, and the session it should have ended signed in.
--
-- Five tables' foreign keys name users, so it is changed in place and never
-- rebuilt: a migration that dropped it would take every message, token and
-- reset with it, at a green boot (patterns/go-data-deletion.md rule 6). ALTER
-- TABLE loses no row.
ALTER TABLE users ADD COLUMN password_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users DROP COLUMN password_changed_at;

-- An emptied session is a row whose data is NULL: the mark a logout leaves, so
-- that a request still in flight cannot commit the session back
-- (patterns/go-auth-sessions.md). The column was NOT NULL and the mark an empty
-- blob. The table is made again rather than altered because none of its rows
-- is kept: every session in it recorded a time, and there is no counter to
-- read out of one. Everybody signs in once more after this runs.
--
-- No foreign key names sessions, its own included, which is what lets a
-- migration drop it (patterns/go-data-deletion.md rule 6).
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

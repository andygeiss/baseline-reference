-- Deleting a person is one statement, and what that statement reaches is
-- declared here rather than remembered in a handler
-- (patterns/go-data-deletion.md).
--
-- Every table that names a user already carried ON DELETE CASCADE. Two things
-- were missing, and both are in this file.

-- 1. The outbox was the row no cascade could reach. It holds an address, which
--    is somebody's data sitting outside users, and nothing pointed back at the
--    account it belongs to. Nullable because a message may go to an address
--    that is nobody's account -- and because SQLite requires a default of NULL
--    on a column added with a REFERENCES clause while foreign keys are on.
ALTER TABLE outbox ADD COLUMN user_id TEXT REFERENCES users(id) ON DELETE CASCADE;

-- 2. Deleting a parent row runs SELECT rowid FROM <child> WHERE <child key> = ?
--    once per child table. Without an index on the child column each of those
--    is a linear scan, and this database has five children of users.
CREATE INDEX outbox_user           ON outbox (user_id);
CREATE INDEX messages_author       ON messages (author_id);
CREATE INDEX attachments_uploader  ON attachments (uploader_id);
CREATE INDEX tokens_user           ON tokens (user_id);
CREATE INDEX resets_user           ON resets (user_id);

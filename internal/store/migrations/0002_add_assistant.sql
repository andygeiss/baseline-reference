-- The assistant posts as a user like anybody else, so every message keeps one
-- author_id and the join that reads a room stays a join.
--
-- Seeding it here rather than registering it at boot is what makes the name
-- unclaimable: users.name_key is UNIQUE, so this row exists before anybody can
-- take it, and boot never has to decide whether an existing row is the one it
-- meant.
--
-- password_hash is NOT NULL and this row must never be able to sign in. "x" is
-- not a PHC-encoded argon2id string, so the verifier rejects it before it
-- compares anything — there is no password that reaches a comparison here.
INSERT INTO users (id, name, name_key, password_hash, created_at)
VALUES ('assistant', 'Assistant', 'assistant', 'x', '2026-08-17T00:00:00Z');

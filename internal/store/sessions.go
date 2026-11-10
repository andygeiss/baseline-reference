package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Sessions is the scs session store for this app.
//
// scs bundles a SQLite store, and it does not fit: it takes one *sql.DB, and
// this app has two pools. LoadAndSave calls Find on every single request, so a
// one-pool store either sends every read through the single write connection —
// serializing the whole app — or writes through the read pool, which brings
// SQLITE_BUSY back. Reads go to the read pool here, writes to the write pool.
type Sessions struct {
	db *DB

	// lifetime is the session manager's Lifetime: how long a deleted token is
	// remembered. See DeleteCtx.
	lifetime time.Duration
}

func NewSessions(db *DB, lifetime time.Duration) *Sessions {
	return &Sessions{db: db, lifetime: lifetime}
}

// FindCtx returns the session data for a token, treating an expired row as
// missing.
//
// The expiry check is this method's job, not the janitor's. scs performs none
// of its own: a store that hands back expired rows keeps those sessions alive,
// and every request refreshes their idle deadline, which switches IdleTimeout
// off without a word.
func (s *Sessions) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var b []byte
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT data FROM sessions WHERE token = ? AND expiry > ? AND length(data) > 0`,
		token, time.Now().Unix()).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("selecting session: %w", err)
	}
	return b, true, nil
}

// CommitCtx stores a session, replacing whatever that token held before —
// unless the token was deleted, in which case it stores nothing. DeleteCtx
// says why.
func (s *Sessions) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	_, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO sessions (token, data, expiry) VALUES (?, ?, ?)
		 ON CONFLICT (token) DO UPDATE SET data = excluded.data, expiry = excluded.expiry
		 WHERE length(sessions.data) > 0`,
		token, b, expiry.Unix())
	if err != nil {
		return fmt.Errorf("committing session: %w", err)
	}
	return nil
}

// DeleteCtx ends a session for good. This is what logout and RenewToken run.
//
// It empties the row and keeps it, because removing it is not enough. A request
// that loaded the session before the logout landed is still in flight, and scs
// commits every loaded session when its handler returns: against a row that is
// simply gone, that commit is an insert, and the logged-out cookie is signed in
// again. Whoever holds a stolen cookie only has to keep one slow request open
// across the owner's logout. So the emptied row stays as the record that this
// token is over — CommitCtx changes nothing on it, FindCtx does not find it —
// until the session's whole lifetime has passed, after which nothing loaded
// under it can still be valid. The janitor then removes it like any expired
// row. scs never commits an empty session, so empty is the mark.
func (s *Sessions) DeleteCtx(ctx context.Context, token string) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO sessions (token, data, expiry) VALUES (?, x'', ?)
		 ON CONFLICT (token) DO UPDATE SET data = x'', expiry = excluded.expiry`,
		token, time.Now().Add(s.lifetime).Unix()); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// scs.CtxStore embeds scs.Store, so these three are required as well. scs hands
// the request context to the Ctx variants by itself, and only reaches these
// when something calls the session manager outside a request.
func (s *Sessions) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}

func (s *Sessions) Commit(token string, b []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, b, expiry)
}

func (s *Sessions) Delete(token string) error { return s.DeleteCtx(context.Background(), token) }

// DeleteExpired reclaims the disk of sessions nobody can use any more. It is
// housekeeping, not enforcement — FindCtx above is what makes an expired
// session stop working, the moment it expires.
func (s *Sessions) DeleteExpired(ctx context.Context) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`DELETE FROM sessions WHERE expiry <= ?`, time.Now().Unix()); err != nil {
		return fmt.Errorf("deleting expired sessions: %w", err)
	}
	return nil
}

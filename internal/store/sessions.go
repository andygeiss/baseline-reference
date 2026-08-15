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
}

func NewSessions(db *DB) *Sessions {
	return &Sessions{db: db}
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
		`SELECT data FROM sessions WHERE token = ? AND expiry > ?`,
		token, time.Now().Unix()).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("selecting session: %w", err)
	}
	return b, true, nil
}

// CommitCtx stores a session, replacing whatever that token held before.
func (s *Sessions) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	_, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO sessions (token, data, expiry) VALUES (?, ?, ?)
		 ON CONFLICT (token) DO UPDATE SET data = excluded.data, expiry = excluded.expiry`,
		token, b, expiry.Unix())
	if err != nil {
		return fmt.Errorf("committing session: %w", err)
	}
	return nil
}

// DeleteCtx removes a session. This is what logout runs, so it has to be a real
// delete rather than an expiry the row keeps.
func (s *Sessions) DeleteCtx(ctx context.Context, token string) error {
	if _, err := s.db.Write.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token); err != nil {
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

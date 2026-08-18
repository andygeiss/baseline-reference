package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Resets struct {
	db *DB
}

func NewResets(db *DB) *Resets {
	return &Resets{db: db}
}

// Add stores a reset link and queues the message that carries it, in one
// transaction.
//
// One transaction is the rule, not an optimisation: a token nobody was told
// about is a dead end the person waits out, and a message promising a token
// that was never stored sends them to a link that cannot work
// (patterns/go-email.md). Neither failure is visible from the outside, so
// neither would ever be reported.
func (s *Resets) Add(ctx context.Context, res *domain.Reset, mail domain.Mail) error {
	if err := mail.Validate(); err != nil {
		return fmt.Errorf("queueing a reset mail: %w", err)
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a reset for user %s: %w", res.UserID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO resets (hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		res.Hash, res.UserID, res.ExpiresAt.UTC().Format(time.RFC3339), now()); err != nil {
		return fmt.Errorf("inserting a reset for user %s: %w", res.UserID, err)
	}
	// user_id is what makes this row reachable by a delete. Without it the
	// address in recipient outlives the account it belongs to
	// (patterns/go-data-deletion.md).
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO outbox (id, user_id, recipient, subject, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		rand.Text(), res.UserID, mail.To, mail.Subject, mail.Text, now()); err != nil {
		return fmt.Errorf("queueing the reset mail for user %s: %w", res.UserID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing a reset for user %s: %w", res.UserID, err)
	}
	return nil
}

// Take spends a reset link: it returns whose account it belongs to and deletes
// it in the same transaction, so a link works exactly once.
//
// An expired row answers ErrNotFound and is deleted anyway. "Expired" and
// "never existed" are the same sentence to whoever is holding a dead link, and
// keeping them apart would tell somebody guessing that they had guessed right.
func (s *Resets) Take(ctx context.Context, hash string, now time.Time) (domain.Reset, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return domain.Reset{}, fmt.Errorf("beginning to take a reset: %w", err)
	}
	defer tx.Rollback()

	var (
		res       domain.Reset
		expiresAt string
	)
	res.Hash = hash
	err = tx.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM resets WHERE hash = ?`, hash,
	).Scan(&res.UserID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Reset{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Reset{}, fmt.Errorf("selecting a reset: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM resets WHERE hash = ?`, hash); err != nil {
		return domain.Reset{}, fmt.Errorf("deleting a spent reset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Reset{}, fmt.Errorf("committing a spent reset: %w", err)
	}

	res.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return domain.Reset{}, fmt.Errorf("parsing the expiry of a reset: %w", err)
	}
	if res.Expired(now) {
		return domain.Reset{}, domain.ErrNotFound
	}
	return res, nil
}

// DeleteExpired clears reset rows nobody can spend any more. This only reclaims
// disk: Take already refuses an expired row.
func (s *Resets) DeleteExpired(ctx context.Context) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`DELETE FROM resets WHERE expires_at < ?`, now()); err != nil {
		return fmt.Errorf("deleting expired resets: %w", err)
	}
	return nil
}

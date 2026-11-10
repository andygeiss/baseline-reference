package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/andygeiss/baseline-reference/v5/internal/domain"
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

// Redeem spends a reset link and sets the password it was issued for, in one
// transaction: the link is read with the user's password counter, the hash is
// written and the counter moved by a statement that names the value read, and
// every reset link the user has is deleted, this one among them. It returns the
// user as the row now stands, for the session the reset signs in.
//
// One transaction because the pieces are one fact. A link deleted in a
// transaction of its own and a hash written in the next leaves, between them, a
// spent link and an unchanged password; and a second link issued before this
// reset would still set the password after it (patterns/go-auth-sessions.md).
//
// The hash is made before this is called, so a link that turns out to be dead
// has cost as much as a live one, and holds the one write connection for no
// longer than three statements.
//
// An unknown, a used and an expired link all answer ErrNotFound, and the
// expired one is deleted anyway. They are the same sentence to whoever is
// holding a dead link, and keeping them apart would tell somebody guessing that
// they had guessed right.
func (s *Resets) Redeem(ctx context.Context, hash, passwordHash string, now time.Time) (domain.User, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, fmt.Errorf("beginning to redeem a reset: %w", err)
	}
	defer tx.Rollback()

	// The link, and the password counter of the user it was issued for: what
	// this request loads, and what its write names below.
	var userID, expiresAt string
	var version int64
	err = tx.QueryRowContext(ctx,
		`SELECT r.user_id, r.expires_at, u.password_version
		   FROM resets r JOIN users u ON u.id = r.user_id
		  WHERE r.hash = ?`, hash,
	).Scan(&userID, &expiresAt, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("selecting a reset: %w", err)
	}
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return domain.User{}, fmt.Errorf("parsing the expiry of a reset: %w", err)
	}
	if (domain.Reset{ExpiresAt: expires}).Expired(now) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM resets WHERE hash = ?`, hash); err != nil {
			return domain.User{}, fmt.Errorf("deleting an expired reset: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.User{}, fmt.Errorf("committing the delete of an expired reset: %w", err)
		}
		return domain.User{}, domain.ErrNotFound
	}

	// It sets a new password, so it moves the counter; a reset link allowed it, so
	// it names the counter this request loaded (patterns/go-auth-sessions.md
	// rule 1). The load is three statements up, in this transaction, on the
	// one write connection: nothing can move the counter in between, so today
	// the predicate cannot refuse on its own. It is the rule's statement all
	// the same, and it is what refuses the day the link is read anywhere else.
	// No row changed is the dead link's answer.
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, password_version = password_version + 1
		  WHERE id = ? AND password_version = ?`,
		passwordHash, userID, version)
	if err != nil {
		return domain.User{}, fmt.Errorf("setting the password of user %s: %w", userID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.User{}, fmt.Errorf("counting the password write of user %s: %w", userID, err)
	}
	if n == 0 {
		return domain.User{}, domain.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM resets WHERE user_id = ?`, userID); err != nil {
		return domain.User{}, fmt.Errorf("deleting the reset links of user %s: %w", userID, err)
	}
	var u domain.User
	if err := tx.QueryRowContext(ctx, userColumns+` WHERE id = ?`, userID).
		Scan(&u.ID, &u.Name, &u.PasswordHash, &u.Email, &u.PasswordVersion); err != nil {
		return domain.User{}, fmt.Errorf("reading user %s after the reset: %w", userID, err)
	}
	if err := tx.Commit(); err != nil {
		return domain.User{}, fmt.Errorf("committing the reset of user %s: %w", userID, err)
	}
	return u, nil
}

// DeleteExpired clears reset rows nobody can spend any more. This only reclaims
// disk: Redeem already refuses an expired row.
func (s *Resets) DeleteExpired(ctx context.Context) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`DELETE FROM resets WHERE expires_at < ?`, now()); err != nil {
		return fmt.Errorf("deleting expired resets: %w", err)
	}
	return nil
}

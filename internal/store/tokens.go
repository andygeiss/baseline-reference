package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Tokens struct {
	db *DB
}

func NewTokens(db *DB) *Tokens {
	return &Tokens{db: db}
}

// Add stores a machine token. hash is the SHA-256 of the secret; the secret
// itself never reaches this package.
func (s *Tokens) Add(ctx context.Context, t *domain.Token) error {
	_, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO tokens (id, user_id, hash, label, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Hash, t.Label, now())
	if err != nil {
		return fmt.Errorf("inserting token %s: %w", t.Label, err)
	}
	return nil
}

// UserByHash returns whoever owns the token with that hash, and marks it used.
func (s *Tokens) UserByHash(ctx context.Context, hash string) (domain.User, error) {
	var u domain.User
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT u.id, u.name, u.password_hash
		   FROM tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.hash = ?`, hash).Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("selecting token: %w", err)
	}

	// Touch it so a person can tell which token to revoke. The WHERE clause
	// keeps this to one write a minute per token instead of one per request:
	// the write pool has a single connection, and every API call would
	// otherwise queue behind the one before it.
	cutoff := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := s.db.Write.ExecContext(ctx,
		`UPDATE tokens SET last_used_at = ?
		  WHERE hash = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now(), hash, cutoff); err != nil {
		return domain.User{}, fmt.Errorf("touching token: %w", err)
	}
	return u, nil
}

// ByUser lists somebody's tokens, newest first. The secret is not there to
// list — only the label, so they can tell one from another.
func (s *Tokens) ByUser(ctx context.Context, userID string) ([]domain.Token, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT id, user_id, label, created_at, COALESCE(last_used_at, '')
		   FROM tokens WHERE user_id = ? ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("selecting tokens: %w", err)
	}
	defer rows.Close()

	var tokens []domain.Token
	for rows.Next() {
		var t domain.Token
		if err := rows.Scan(&t.ID, &t.UserID, &t.Label, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scanning token: %w", err)
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading tokens: %w", err)
	}
	return tokens, nil
}

// Delete revokes one of somebody's tokens. The user ID is in the WHERE clause,
// so guessing another person's token ID revokes nothing.
func (s *Tokens) Delete(ctx context.Context, userID, id string) error {
	res, err := s.db.Write.ExecContext(ctx,
		`DELETE FROM tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("deleting token %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the deleted rows of token %s: %w", id, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

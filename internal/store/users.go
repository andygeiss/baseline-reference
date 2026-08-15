package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Users struct {
	db *DB
}

func NewUsers(db *DB) *Users {
	return &Users{db: db}
}

// nameKey is what UNIQUE guards in the users table: the name, folded to lower
// case by Go so the whole of Unicode folds, not only ASCII.
func nameKey(name string) string { return strings.ToLower(name) }

// Add stores a new user, reporting ErrNameTaken when the name is already
// somebody's.
//
// The check and the insert share one transaction on the single-writer pool.
// Reading the driver's error string would be the other way to spot the clash,
// and that string is not part of any contract.
func (s *Users) Add(ctx context.Context, u *domain.User) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning add of user %s: %w", u.Name, err)
	}
	defer tx.Rollback()

	var taken bool
	err = tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE name_key = ?)`, nameKey(u.Name)).Scan(&taken)
	if err != nil {
		return fmt.Errorf("checking the name of user %s: %w", u.Name, err)
	}
	if taken {
		return domain.ErrNameTaken
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, name, name_key, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Name, nameKey(u.Name), u.PasswordHash, now()); err != nil {
		return fmt.Errorf("inserting user %s: %w", u.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing user %s: %w", u.Name, err)
	}
	return nil
}

// ByName finds a user by name, ignoring capitals.
func (s *Users) ByName(ctx context.Context, name string) (domain.User, error) {
	return s.one(ctx, `SELECT id, name, password_hash FROM users WHERE name_key = ?`, nameKey(name))
}

// ByID finds a user by ID.
func (s *Users) ByID(ctx context.Context, id string) (domain.User, error) {
	return s.one(ctx, `SELECT id, name, password_hash FROM users WHERE id = ?`, id)
}

// UpdatePasswordHash replaces somebody's stored hash. Login calls it when the
// stored hash was written under weaker argon2id settings than today's.
func (s *Users) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
		return fmt.Errorf("updating the password hash of user %s: %w", id, err)
	}
	return nil
}

func (s *Users) one(ctx context.Context, query string, arg any) (domain.User, error) {
	var u domain.User
	err := s.db.Read.QueryRowContext(ctx, query, arg).Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("selecting user: %w", err)
	}
	return u, nil
}

// now is the one place a stored timestamp is made, so every table spells it the
// same way.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

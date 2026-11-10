package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andygeiss/baseline-reference/v5/internal/domain"
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
//
// Its own errors name the user by id, never by name: an error's text is logged
// by whoever handles it (patterns/go-errors-logging.md, Never log).
func (s *Users) Add(ctx context.Context, u *domain.User) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning add of user %s: %w", u.ID, err)
	}
	defer tx.Rollback()

	var taken bool
	err = tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE name_key = ?)`, nameKey(u.Name)).Scan(&taken)
	if err != nil {
		return fmt.Errorf("checking the name of user %s: %w", u.ID, err)
	}
	if taken {
		return domain.ErrNameTaken
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, name, name_key, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Name, nameKey(u.Name), u.PasswordHash, now()); err != nil {
		return fmt.Errorf("inserting user %s: %w", u.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing user %s: %w", u.ID, err)
	}
	return nil
}

// ByName finds a user by name, ignoring capitals.
func (s *Users) ByName(ctx context.Context, name string) (domain.User, error) {
	return s.one(ctx, userColumns+` WHERE name_key = ?`, nameKey(name))
}

// ByID finds a user by ID.
func (s *Users) ByID(ctx context.Context, id string) (domain.User, error) {
	return s.one(ctx, userColumns+` WHERE id = ?`, id)
}

// Rehash writes a stronger hash of a password over the hash a sign-in loaded
// and has just checked that password against.
//
// It sets no password, so it moves no counter: the password is the one every
// session of this user signed in with, and a statement that moved the counter
// here would sign all of them out the first time the argon2id settings were
// raised (patterns/go-auth-sessions.md rule 1).
//
// It is a statement a password allowed, so it names the counter its request
// loaded. A request loads the row when its headers arrive and may write
// minutes later, its body held open the whole time: written by id alone, that
// late write would put the old password's hash over a reset that landed in
// between — the reset somebody made because the old password was known. No row
// changed means exactly that happened, and the answer is domain.ErrStale: a
// refusal, never a retry.
func (s *Users) Rehash(ctx context.Context, id, hash string, version int64) error {
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ? AND password_version = ?`, hash, id, version)
	if err != nil {
		return fmt.Errorf("rehashing the password of user %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rehash of user %s: %w", id, err)
	}
	if n == 0 {
		return domain.ErrStale
	}
	return nil
}

// SetEmail stores somebody's address, or clears it when email is empty.
func (s *Users) SetEmail(ctx context.Context, id, email string) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`UPDATE users SET email = ? WHERE id = ?`, email, id); err != nil {
		return fmt.Errorf("setting the email of user %s: %w", id, err)
	}
	return nil
}

// Delete erases an account and everything the schema hangs off it.
//
// Every table that names a user declares its own ON DELETE, so the cascade is
// the schema keeping a promise rather than a list of deletes here that the next
// table added would be missing from (patterns/go-data-deletion.md).
//
// One delete the schema cannot declare comes first, in the same transaction:
// an outbox row queued with no user_id — mail to an address before it was
// anybody's account — names this person by address alone, and no cascade
// follows an address. An account with no address stored matches nothing,
// because no message is queued to an empty one.
//
// The id is the actor's own, out of the session: this app has no
// administrator, so the only account anybody may delete is the one they are
// signed in as, and there is no second predicate to forget.
//
// version is the password counter as the request read it, and the DELETE names
// it: the delete is allowed by a password checked against the row that request
// loaded, and a reset that landed since has made that check worthless
// (patterns/go-data-deletion.md, Deleting is a write). No row changed is then
// one of two things, and one read inside this transaction says which: the
// account is already gone, domain.ErrNotFound, or it has moved on,
// domain.ErrStale — a refusal, and the rollback puts the queued mail back.
func (s *Users) Delete(ctx context.Context, id string, version int64) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning to delete user %s: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM outbox
		  WHERE user_id IS NULL
		    AND recipient = (SELECT email FROM users WHERE id = ?)`, id); err != nil {
		return fmt.Errorf("deleting the mail queued to user %s by address: %w", id, err)
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM users WHERE id = ? AND password_version = ?`, id, version)
	if err != nil {
		return fmt.Errorf("deleting user %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the deletion of user %s: %w", id, err)
	}
	if n == 0 {
		var there bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE id = ?)`, id).Scan(&there); err != nil {
			return fmt.Errorf("looking for user %s after a delete that changed no row: %w", id, err)
		}
		if there {
			return domain.ErrStale
		}
		return domain.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing the deletion of user %s: %w", id, err)
	}
	return nil
}

// userColumns is the one shape every user read returns, so a column added to
// the table is added in one place.
const userColumns = `SELECT id, name, password_hash, email, password_version FROM users`

func (s *Users) one(ctx context.Context, query string, arg any) (domain.User, error) {
	var u domain.User
	err := s.db.Read.QueryRowContext(ctx, query, arg).
		Scan(&u.ID, &u.Name, &u.PasswordHash, &u.Email, &u.PasswordVersion)
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

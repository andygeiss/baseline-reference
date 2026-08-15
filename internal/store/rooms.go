package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Rooms struct {
	db *DB
}

func NewRooms(db *DB) *Rooms {
	return &Rooms{db: db}
}

// Add stores a new room, reporting ErrSlugTaken when one already answers to
// that address. Two names can slug to the same thing — "Go Chat" and
// "go chat" — so the clash is about the slug, not the name.
func (s *Rooms) Add(ctx context.Context, r *domain.Room) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning add of room %s: %w", r.Slug, err)
	}
	defer tx.Rollback()

	var taken bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM rooms WHERE slug = ?)`, r.Slug).Scan(&taken); err != nil {
		return fmt.Errorf("checking the slug of room %s: %w", r.Slug, err)
	}
	if taken {
		return domain.ErrSlugTaken
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO rooms (id, slug, name, created_at) VALUES (?, ?, ?, ?)`,
		r.ID, r.Slug, r.Name, now()); err != nil {
		return fmt.Errorf("inserting room %s: %w", r.Slug, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing room %s: %w", r.Slug, err)
	}
	return nil
}

// All returns every room, newest conversation first, with the number of
// messages in each.
func (s *Rooms) All(ctx context.Context) ([]domain.Room, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT id, slug, name FROM rooms ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("selecting rooms: %w", err)
	}
	defer rows.Close()

	var rooms []domain.Room
	for rows.Next() {
		var r domain.Room
		if err := rows.Scan(&r.ID, &r.Slug, &r.Name); err != nil {
			return nil, fmt.Errorf("scanning room: %w", err)
		}
		rooms = append(rooms, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading rooms: %w", err)
	}
	return rooms, nil
}

// BySlug finds the room a URL names.
func (s *Rooms) BySlug(ctx context.Context, slug string) (domain.Room, error) {
	var r domain.Room
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT id, slug, name FROM rooms WHERE slug = ?`, slug).Scan(&r.ID, &r.Slug, &r.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Room{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Room{}, fmt.Errorf("selecting room %s: %w", slug, err)
	}
	return r, nil
}

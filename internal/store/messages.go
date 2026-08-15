package store

import (
	"context"
	"fmt"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Messages struct {
	db *DB
}

func NewMessages(db *DB) *Messages {
	return &Messages{db: db}
}

// Add stores a message and fills in the sequence number SQLite gave it. That
// number is the cursor every reader polls with, so the caller needs it back.
func (s *Messages) Add(ctx context.Context, m *domain.Message) error {
	m.CreatedAt = time.Now().UTC()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO messages (room_id, author_id, body, created_at) VALUES (?, ?, ?, ?)`,
		m.RoomID, m.AuthorID, m.Body, m.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("inserting message in room %s: %w", m.RoomID, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading the sequence of the new message in room %s: %w", m.RoomID, err)
	}
	m.Seq = seq
	return nil
}

// Since returns up to MaxPageLen messages posted after the cursor, oldest
// first — the answer to one poll.
func (s *Messages) Since(ctx context.Context, roomID string, since int64) ([]domain.Message, error) {
	return s.query(ctx,
		`SELECT m.seq, m.room_id, m.author_id, u.name, m.body, m.created_at
		   FROM messages m JOIN users u ON u.id = m.author_id
		  WHERE m.room_id = ? AND m.seq > ?
		  ORDER BY m.seq
		  LIMIT ?`,
		roomID, since, domain.MaxPageLen)
}

// Recent returns the last limit messages of a room, oldest first — what a
// reader sees when the page first paints.
func (s *Messages) Recent(ctx context.Context, roomID string, limit int) ([]domain.Message, error) {
	if limit > domain.MaxPageLen {
		limit = domain.MaxPageLen
	}
	// The newest rows are the cheap end of the index, so the query walks
	// backwards and the result is turned around afterwards.
	msgs, err := s.query(ctx,
		`SELECT m.seq, m.room_id, m.author_id, u.name, m.body, m.created_at
		   FROM messages m JOIN users u ON u.id = m.author_id
		  WHERE m.room_id = ?
		  ORDER BY m.seq DESC
		  LIMIT ?`,
		roomID, limit)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *Messages) query(ctx context.Context, query string, args ...any) ([]domain.Message, error) {
	rows, err := s.db.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("selecting messages: %w", err)
	}
	defer rows.Close()

	var msgs []domain.Message
	for rows.Next() {
		var (
			m         domain.Message
			createdAt string
		)
		if err := rows.Scan(&m.Seq, &m.RoomID, &m.AuthorID, &m.Author, &m.Body, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		m.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parsing the time of message %d: %w", m.Seq, err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading messages: %w", err)
	}
	return msgs, nil
}

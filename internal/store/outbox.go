package store

import (
	"context"
	"fmt"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Outbox struct {
	db *DB
}

func NewOutbox(db *DB) *Outbox {
	return &Outbox{db: db}
}

// MaxSendAttempts is how often one message is retried before it is left alone.
// A relay that has refused a message five times is not going to accept it on
// the sixth, and a row that retries forever is a log line every tick.
const MaxSendAttempts = 5

// Queued is one message waiting to go out, with the id the sender marks it by.
type Queued struct {
	ID   string
	Mail domain.Mail
}

// Unsent returns messages that have not gone out and have attempts left, oldest
// first.
func (s *Outbox) Unsent(ctx context.Context, limit int) ([]Queued, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT id, recipient, subject, body
		   FROM outbox
		  WHERE sent_at IS NULL AND attempts < ?
		  ORDER BY created_at
		  LIMIT ?`, MaxSendAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("selecting unsent mail: %w", err)
	}
	defer rows.Close()

	var queued []Queued
	for rows.Next() {
		var q Queued
		if err := rows.Scan(&q.ID, &q.Mail.To, &q.Mail.Subject, &q.Mail.Text); err != nil {
			return nil, fmt.Errorf("scanning unsent mail: %w", err)
		}
		queued = append(queued, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading unsent mail: %w", err)
	}
	return queued, nil
}

// Sent marks a message as delivered to the relay. The body stays: it holds a
// link that expires within the hour, and knowing what was sent is what makes a
// "no mail arrived" report answerable.
func (s *Outbox) Sent(ctx context.Context, id string) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`UPDATE outbox SET sent_at = ? WHERE id = ?`, now(), id); err != nil {
		return fmt.Errorf("marking mail %s sent: %w", id, err)
	}
	return nil
}

// Failed records one unsuccessful attempt. Past MaxSendAttempts the row stops
// being picked up.
func (s *Outbox) Failed(ctx context.Context, id string) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`UPDATE outbox SET attempts = attempts + 1 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("recording a failed send of mail %s: %w", id, err)
	}
	return nil
}

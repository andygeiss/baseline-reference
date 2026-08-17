package store

import (
	"context"
	"database/sql"
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

// columns is the one shape every read below returns. The attachment is a LEFT
// JOIN rather than a second query: at most one file hangs on a message, so
// reading a page of them stays one round trip.
const columns = `SELECT m.seq, m.room_id, m.author_id, u.name, m.body, m.created_at,
                        a.id, a.uploader_id, a.name, a.kind, a.size
                   FROM messages m
                   JOIN users u ON u.id = m.author_id
                   LEFT JOIN attachments a ON a.message_seq = m.seq`

// Add stores a message and fills in the sequence number SQLite gave it. That
// number is the cursor every reader polls with, so the caller needs it back.
//
// blob is the attachment's bytes and m.Attachment describes them; both are
// absent when the message carries no file. They are one transaction on purpose:
// a message whose file failed to store is a broken picture nobody can tell from
// one that was never sent.
func (s *Messages) Add(ctx context.Context, m *domain.Message, blob []byte) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning add of a message in room %s: %w", m.RoomID, err)
	}
	defer tx.Rollback()

	m.CreatedAt = time.Now().UTC()
	res, err := tx.ExecContext(ctx,
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

	if m.Attachment != nil {
		m.Attachment.MessageSeq = seq
		m.Attachment.CreatedAt = m.CreatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO attachments (id, message_seq, uploader_id, name, kind, size, bytes, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			m.Attachment.ID, seq, m.Attachment.UploaderID, m.Attachment.Name,
			m.Attachment.Kind, m.Attachment.Size, blob,
			m.Attachment.CreatedAt.Format(time.RFC3339)); err != nil {
			return fmt.Errorf("inserting the attachment of message %d: %w", seq, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing a message in room %s: %w", m.RoomID, err)
	}
	return nil
}

// Since returns up to MaxPageLen messages posted after the cursor, oldest
// first — the answer to one poll.
func (s *Messages) Since(ctx context.Context, roomID string, since int64) ([]domain.Message, error) {
	return s.query(ctx,
		columns+`
		  WHERE m.room_id = ? AND m.seq > ?
		  ORDER BY m.seq
		  LIMIT ?`,
		roomID, since, domain.MaxPageLen)
}

// Page returns up to limit messages older than the cursor, oldest first.
// before == 0 starts at the newest message, which is what a room's first paint
// asks for.
//
// This is keyset paging, and the reason is correctness before speed: OFFSET
// counts rows that are still moving, so one message arriving between two pages
// makes a reader see another one twice. seq is unique and only grows, so a
// cursor on it names a place in the data rather than a distance from the end.
func (s *Messages) Page(ctx context.Context, roomID string, before int64, limit int) ([]domain.Message, error) {
	if limit > domain.MaxPageLen {
		limit = domain.MaxPageLen
	}
	// Two statements rather than one carrying an "unless the cursor is zero"
	// clause: each is a plain range scan of messages_room_seq, and neither asks
	// the planner to reason about a parameter it cannot see.
	//
	// Both walk backwards, because the newest rows are the cheap end of that
	// index, and the result is turned around afterwards.
	var (
		msgs []domain.Message
		err  error
	)
	if before > 0 {
		msgs, err = s.query(ctx,
			columns+`
			  WHERE m.room_id = ? AND m.seq < ?
			  ORDER BY m.seq DESC
			  LIMIT ?`,
			roomID, before, limit)
	} else {
		msgs, err = s.query(ctx,
			columns+`
			  WHERE m.room_id = ?
			  ORDER BY m.seq DESC
			  LIMIT ?`,
			roomID, limit)
	}
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
			// The LEFT JOIN leaves every attachment column NULL for a message
			// with no file, so each one is scanned as a nullable.
			attID, attUploader, attName, attKind sql.NullString
			attSize                              sql.NullInt64
		)
		if err := rows.Scan(&m.Seq, &m.RoomID, &m.AuthorID, &m.Author, &m.Body, &createdAt,
			&attID, &attUploader, &attName, &attKind, &attSize); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		m.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parsing the time of message %d: %w", m.Seq, err)
		}
		if attID.Valid {
			m.Attachment = &domain.Attachment{
				ID:         attID.String,
				MessageSeq: m.Seq,
				UploaderID: attUploader.String,
				Name:       attName.String,
				Kind:       attKind.String,
				Size:       attSize.Int64,
				CreatedAt:  m.CreatedAt,
			}
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading messages: %w", err)
	}
	return msgs, nil
}

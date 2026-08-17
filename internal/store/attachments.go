package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Attachments struct {
	db *DB
}

func NewAttachments(db *DB) *Attachments {
	return &Attachments{db: db}
}

// blob is what Open hands back: a reader over bytes already in memory, with a
// Close that has nothing to close. http.ServeContent wants a ReadSeeker so it
// can answer a range request, and a *sql.Rows cannot seek.
type blob struct{ *bytes.Reader }

func (blob) Close() error { return nil }

// Open returns an attachment and its bytes.
//
// There is no actor in this signature, and that is deliberate: an attachment is
// readable by everyone who can read the room it was posted in, which in this
// app is everyone signed in. Rooms and their messages are shared rows — the
// README says so under *Who owns what* — so the predicate that guards this read
// is the session, and requireAuth applies it. Deleting is not shared, and
// Delete below carries the predicate that says so.
//
// The whole file is read into memory. It is capped at domain.MaxAttachmentBytes
// two layers up, and streaming a BLOB out of SQLite means holding a row open
// for the length of somebody's download.
func (s *Attachments) Open(ctx context.Context, id string) (domain.Attachment, io.ReadSeekCloser, error) {
	var (
		a         domain.Attachment
		bs        []byte
		createdAt string
	)
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT id, message_seq, uploader_id, name, kind, size, bytes, created_at
		   FROM attachments WHERE id = ?`, id,
	).Scan(&a.ID, &a.MessageSeq, &a.UploaderID, &a.Name, &a.Kind, &a.Size, &bs, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attachment{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return domain.Attachment{}, nil, fmt.Errorf("selecting attachment %s: %w", id, err)
	}
	a.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return domain.Attachment{}, nil, fmt.Errorf("parsing the time of attachment %s: %w", id, err)
	}
	return a, blob{bytes.NewReader(bs)}, nil
}

// Delete removes an attachment that belongs to this uploader, reporting
// ErrNotFound when no such row is theirs.
//
// The ownership test is the second term of the WHERE clause, not a comparison
// in Go after a read: one statement decides, and RowsAffected reports what it
// decided. Somebody else's file and a file that never existed answer the same
// way, so the id space says nothing about what exists
// (patterns/go-authorization.md).
func (s *Attachments) Delete(ctx context.Context, uploaderID, id string) error {
	res, err := s.db.Write.ExecContext(ctx,
		`DELETE FROM attachments WHERE id = ? AND uploader_id = ?`, id, uploaderID)
	if err != nil {
		return fmt.Errorf("deleting attachment %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the deletion of attachment %s: %w", id, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

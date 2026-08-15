package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/andygeiss/baseline-reference/internal/domain"
)

type Tasks struct {
	db *DB
}

func NewTasks(db *DB) *Tasks {
	return &Tasks{db: db}
}

// Add stores a new task at the end of the list.
func (s *Tasks) Add(ctx context.Context, t *domain.Task) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO tasks (id, title, done, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.Title, t.Done, now, now)
	if err != nil {
		return fmt.Errorf("inserting task %s: %w", t.ID, err)
	}
	return nil
}

// All returns the whole list: open tasks first, each group in the order it was
// added. The order comes from seq, not from a timestamp — two tasks added in
// the same second would otherwise sort at random.
func (s *Tasks) All(ctx context.Context) ([]domain.Task, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT id, title, done FROM tasks ORDER BY done, seq`)
	if err != nil {
		return nil, fmt.Errorf("selecting tasks: %w", err)
	}
	defer rows.Close()

	var tasks []domain.Task
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Done); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading tasks: %w", err)
	}
	return tasks, nil
}

// Update loads the task, applies change to it, and stores the result — all in
// one write transaction. Read and write must be atomic: two taps arriving
// together would otherwise both read the same task, and the second write would
// undo the first. The write pool has a single connection, so these transactions
// serialize instead of colliding.
func (s *Tasks) Update(ctx context.Context, id string, change func(*domain.Task) error) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning update of task %s: %w", id, err)
	}
	defer tx.Rollback()

	var t domain.Task
	err = tx.QueryRowContext(ctx,
		`SELECT id, title, done FROM tasks WHERE id = ?`, id).Scan(&t.ID, &t.Title, &t.Done)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("selecting task %s: %w", id, err)
	}
	if err := change(&t); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET title = ?, done = ?, updated_at = ? WHERE id = ?`,
		t.Title, t.Done, time.Now().UTC().Format(time.RFC3339), id); err != nil {
		return fmt.Errorf("updating task %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing update of task %s: %w", id, err)
	}
	return nil
}

// Delete removes one task, reporting ErrNotFound when the list no longer has it.
func (s *Tasks) Delete(ctx context.Context, id string) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting task %s: %w", id, err)
	}
	// The driver counts the rows, so this says whether the task was there —
	// a DELETE that matches nothing is not an error to SQLite.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the deleted rows of task %s: %w", id, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

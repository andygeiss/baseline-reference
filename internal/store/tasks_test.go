package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/andygeiss/baseline-reference/internal/domain"
)

// newTestDB opens a real SQLite file with production pragmas and migrations.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// add stores a task, failing the test if the store refuses it.
func add(t *testing.T, tasks *Tasks, id, title string) {
	t.Helper()
	task, err := domain.NewTask(id, title)
	if err != nil {
		t.Fatalf("building task %s: %v", id, err)
	}
	if err := tasks.Add(t.Context(), task); err != nil {
		t.Fatalf("adding task %s: %v", id, err)
	}
}

// titles reads the list as it would be rendered.
func titles(t *testing.T, tasks *Tasks) []string {
	t.Helper()
	all, err := tasks.All(t.Context())
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	got := make([]string, len(all))
	for i, task := range all {
		got[i] = task.Title
	}
	return got
}

func TestTasks_AddAndList(t *testing.T) {
	t.Parallel()
	tasks := NewTasks(newTestDB(t))
	ctx := t.Context()

	if got := titles(t, tasks); len(got) != 0 {
		t.Errorf("a new list holds %v, want nothing", got)
	}

	add(t, tasks, "t1", "Buy milk")
	add(t, tasks, "t2", "Call Ada")

	all, err := tasks.All(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d tasks, want 2", len(all))
	}
	if all[0] != (domain.Task{ID: "t1", Title: "Buy milk", Done: false}) {
		t.Errorf("first task = %+v", all[0])
	}
}

func TestTasks_ListOrdersOpenFirstThenByAge(t *testing.T) {
	t.Parallel()
	tasks := NewTasks(newTestDB(t))
	ctx := t.Context()

	for _, task := range []struct{ id, title string }{
		{"t1", "first"}, {"t2", "second"}, {"t3", "third"},
	} {
		add(t, tasks, task.id, task.title)
	}
	if err := tasks.Update(ctx, "t1", func(task *domain.Task) error { task.Toggle(); return nil }); err != nil {
		t.Fatalf("toggling t1: %v", err)
	}

	got := titles(t, tasks)
	want := []string{"second", "third", "first"} // done sinks; the rest keep their order
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestTasks_ToggleAndDelete(t *testing.T) {
	t.Parallel()
	tasks := NewTasks(newTestDB(t))
	ctx := t.Context()
	add(t, tasks, "t1", "Buy milk")

	if err := tasks.Update(ctx, "t1", func(task *domain.Task) error { task.Toggle(); return nil }); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	all, err := tasks.All(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if !all[0].Done {
		t.Error("the toggle was not persisted")
	}

	if err := tasks.Delete(ctx, "t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := titles(t, tasks); len(got) != 0 {
		t.Errorf("after the delete the list holds %v, want nothing", got)
	}
}

func TestTasks_UpdateRollsBackWhenTheChangeFails(t *testing.T) {
	t.Parallel()
	tasks := NewTasks(newTestDB(t))
	ctx := t.Context()
	add(t, tasks, "t1", "Buy milk")

	refused := errors.New("refused")
	err := tasks.Update(ctx, "t1", func(task *domain.Task) error {
		task.Title = "Rewritten" // the transaction must not carry this to disk
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("got %v, want the change's own error", err)
	}

	if got := titles(t, tasks); got[0] != "Buy milk" {
		t.Errorf("a refused change was persisted: %v", got)
	}
}

// Concurrent changes must not lose one: the store applies each inside its own
// write transaction, so the second sees the first's row.
func TestTasks_ConcurrentUpdatesSerialize(t *testing.T) {
	t.Parallel()
	tasks := NewTasks(newTestDB(t))
	ctx := t.Context()
	add(t, tasks, "t1", "Buy milk")
	add(t, tasks, "t2", "Call Ada")

	var wg sync.WaitGroup
	for _, id := range []string{"t1", "t2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tasks.Update(ctx, id, func(task *domain.Task) error {
				task.Toggle()
				return nil
			}); err != nil {
				t.Errorf("toggling %s: %v", id, err)
			}
		}()
	}
	wg.Wait()

	all, err := tasks.All(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if n := domain.Open(all); n != 0 {
		t.Errorf("%d tasks are still open: a toggle was lost", n)
	}
}

func TestTasks_NotFound(t *testing.T) {
	t.Parallel()
	tasks := NewTasks(newTestDB(t))
	ctx := t.Context()

	if err := tasks.Update(ctx, "missing", func(*domain.Task) error { return nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update: got %v, want domain.ErrNotFound", err)
	}
	if err := tasks.Delete(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete: got %v, want domain.ErrNotFound", err)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.db")
	for range 2 { // opening twice must not re-apply migrations
		db, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		db.Close()
	}
}

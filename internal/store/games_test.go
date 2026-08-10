package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/andygeiss/tictactoe/internal/domain"
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

func TestGames_CreateGetUpdate(t *testing.T) {
	t.Parallel()
	games := NewGames(newTestDB(t))
	ctx := t.Context()

	g := domain.NewGame("g1")
	if err := games.Create(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := games.Get(ctx, "g1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if *got != *g {
		t.Errorf("get after create: got %+v, want %+v", got, g)
	}

	if err := g.Move(4); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := games.Update(ctx, g); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = games.Get(ctx, "g1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Board[4] != domain.PlayerX || got.Next != domain.PlayerO {
		t.Errorf("update not persisted: %+v", got)
	}
}

func TestGames_NotFound(t *testing.T) {
	t.Parallel()
	games := NewGames(newTestDB(t))
	ctx := t.Context()

	if _, err := games.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get: got %v, want ErrNotFound", err)
	}
	if err := games.Update(ctx, domain.NewGame("missing")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update: got %v, want ErrNotFound", err)
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

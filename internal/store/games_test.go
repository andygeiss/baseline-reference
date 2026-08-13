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

	if _, err := games.Update(ctx, "g1", func(g *domain.Game) error { return g.Move(4) }); err != nil {
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

func TestGames_UpdateRollsBackOnRuleViolation(t *testing.T) {
	t.Parallel()
	games := NewGames(newTestDB(t))
	ctx := t.Context()

	g := domain.NewGame("g1")
	if err := games.Create(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := games.Update(ctx, "g1", func(g *domain.Game) error { return g.Move(4) }); err != nil {
		t.Fatalf("first move: %v", err)
	}

	got, err := games.Update(ctx, "g1", func(g *domain.Game) error { return g.Move(4) })
	if !errors.Is(err, domain.ErrCellTaken) {
		t.Fatalf("second move on the same cell: got %v, want ErrCellTaken", err)
	}
	if got == nil || got.Next != domain.PlayerO {
		t.Errorf("rule violation must return the current game, got %+v", got)
	}

	stored, err := games.Get(ctx, "g1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Next != domain.PlayerO {
		t.Errorf("rejected move was persisted: %+v", stored)
	}
}

// Concurrent moves must not lose one: the store applies each inside its own
// write transaction, so the second sees the first's board.
func TestGames_ConcurrentUpdatesSerialize(t *testing.T) {
	t.Parallel()
	games := NewGames(newTestDB(t))
	ctx := t.Context()

	if err := games.Create(ctx, domain.NewGame("g1")); err != nil {
		t.Fatalf("create: %v", err)
	}

	var wg sync.WaitGroup
	for _, cell := range []int{0, 4} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := games.Update(ctx, "g1", func(g *domain.Game) error { return g.Move(cell) }); err != nil {
				t.Errorf("move %d: %v", cell, err)
			}
		}()
	}
	wg.Wait()

	got, err := games.Get(ctx, "g1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Board[0] == domain.PlayerNone || got.Board[4] == domain.PlayerNone {
		t.Errorf("a move was lost: %q", got.Board)
	}
}

func TestGames_NotFound(t *testing.T) {
	t.Parallel()
	games := NewGames(newTestDB(t))
	ctx := t.Context()

	if _, err := games.Get(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get: got %v, want domain.ErrNotFound", err)
	}
	if _, err := games.Update(ctx, "missing", func(*domain.Game) error { return nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update: got %v, want domain.ErrNotFound", err)
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

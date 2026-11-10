package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestPruneSnapshotsKeepsTheNewest is the half of a delete this binary owns: a
// snapshot holds everyone deleted since it was written, so the old ones go
// (patterns/go-data-deletion.md). It also deletes files, so the other half of
// the test is everything it must leave alone.
func TestPruneSnapshotsKeepsTheNewest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{
		"snapshot-2026-11-06.db",
		"snapshot-2026-11-07.db",
		"snapshot-2026-11-08.db",
		"snapshot-2026-11-09.db",
		"snapshot-2026-11-10.db",
		"snapshot-notes.db",          // the prefix and the suffix, and no date
		"snapshot-2026-11-01.db.bak", // somebody's copy
		"app.db",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with a snapshot's name is not a snapshot.
	if err := os.Mkdir(filepath.Join(dir, "snapshot-2026-11-02.db"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := pruneSnapshots(dir, 3); err != nil {
		t.Fatalf("pruneSnapshots: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{
		"app.db",
		"snapshot-2026-11-01.db.bak",
		"snapshot-2026-11-02.db",
		"snapshot-2026-11-08.db",
		"snapshot-2026-11-09.db",
		"snapshot-2026-11-10.db",
		"snapshot-notes.db",
	}
	if !slices.Equal(got, want) {
		t.Errorf("after pruning to three:\n got %v\nwant %v", got, want)
	}
}

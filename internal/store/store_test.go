package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
	"github.com/andygeiss/baseline-reference/v3/internal/store"
)

// newTestDB opens a real database in a temporary directory, with the production
// pragmas and migrations. The SQL is the unit under test here, so there is
// nothing to fake.
func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func addUser(t *testing.T, db *store.DB, name string) domain.User {
	t.Helper()
	users := store.NewUsers(db)
	u := &domain.User{ID: "user-" + name, Name: name, PasswordHash: "hash"}
	if err := users.Add(t.Context(), u); err != nil {
		t.Fatalf("adding user %s: %v", name, err)
	}
	return *u
}

func addRoom(t *testing.T, db *store.DB, name string) domain.Room {
	t.Helper()
	rooms := store.NewRooms(db)
	r, err := domain.NewRoom("room-"+name, name)
	if err != nil {
		t.Fatalf("building room %s: %v", name, err)
	}
	if err := rooms.Add(t.Context(), r); err != nil {
		t.Fatalf("adding room %s: %v", name, err)
	}
	return *r
}

func TestUsers(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	users := store.NewUsers(db)
	ada := addUser(t, db, "Ada")

	t.Run("found by name, ignoring capitals", func(t *testing.T) {
		got, err := users.ByName(t.Context(), "ADA")
		if err != nil {
			t.Fatalf("ByName: %v", err)
		}
		if got.ID != ada.ID {
			t.Errorf("ID = %q, want %q", got.ID, ada.ID)
		}
	})

	t.Run("found by ID", func(t *testing.T) {
		got, err := users.ByID(t.Context(), ada.ID)
		if err != nil || got.Name != "Ada" {
			t.Errorf("ByID = %v, %v; want Ada", got, err)
		}
	})

	t.Run("an unknown name is not found", func(t *testing.T) {
		_, err := users.ByName(t.Context(), "Nobody")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("a name differing only by capitals is taken", func(t *testing.T) {
		err := users.Add(t.Context(), &domain.User{ID: "other", Name: "ada", PasswordHash: "hash"})
		if !errors.Is(err, domain.ErrNameTaken) {
			t.Errorf("err = %v, want ErrNameTaken", err)
		}
	})

	t.Run("the password hash can be replaced", func(t *testing.T) {
		if err := users.UpdatePasswordHash(t.Context(), ada.ID, "stronger"); err != nil {
			t.Fatalf("UpdatePasswordHash: %v", err)
		}
		got, err := users.ByID(t.Context(), ada.ID)
		if err != nil || got.PasswordHash != "stronger" {
			t.Errorf("hash = %q (%v), want stronger", got.PasswordHash, err)
		}
	})
}

func TestRooms(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	rooms := store.NewRooms(db)
	addRoom(t, db, "General")

	t.Run("found by slug", func(t *testing.T) {
		got, err := rooms.BySlug(t.Context(), "general")
		if err != nil || got.Name != "General" {
			t.Errorf("BySlug = %v, %v; want General", got, err)
		}
	})

	t.Run("an unknown slug is not found", func(t *testing.T) {
		_, err := rooms.BySlug(t.Context(), "nowhere")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("the same address twice is refused", func(t *testing.T) {
		err := rooms.Add(t.Context(), &domain.Room{ID: "other", Slug: "general", Name: "general"})
		if !errors.Is(err, domain.ErrSlugTaken) {
			t.Errorf("err = %v, want ErrSlugTaken", err)
		}
	})

	t.Run("listed by name", func(t *testing.T) {
		addRoom(t, db, "Announcements")
		got, err := rooms.All(t.Context())
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		if len(got) != 2 || got[0].Name != "Announcements" {
			t.Errorf("All = %v, want Announcements first", got)
		}
	})
}

func TestMessages(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	messages := store.NewMessages(db)
	ada := addUser(t, db, "Ada")
	general := addRoom(t, db, "General")
	other := addRoom(t, db, "Other")

	post := func(room domain.Room, body string) domain.Message {
		t.Helper()
		m := &domain.Message{RoomID: room.ID, AuthorID: ada.ID, Body: body}
		if err := messages.Add(t.Context(), m); err != nil {
			t.Fatalf("adding message: %v", err)
		}
		return *m
	}

	first := post(general, "one")
	second := post(general, "two")
	post(other, "elsewhere")

	t.Run("the sequence comes back on the message", func(t *testing.T) {
		// The caller needs it: that number is the cursor every reader polls with.
		if first.Seq == 0 || second.Seq <= first.Seq {
			t.Errorf("sequences = %d, %d; want the second to be higher and neither zero",
				first.Seq, second.Seq)
		}
	})

	t.Run("Since brings back only what is newer, in one room", func(t *testing.T) {
		got, err := messages.Since(t.Context(), general.ID, first.Seq)
		if err != nil {
			t.Fatalf("Since: %v", err)
		}
		if len(got) != 1 || got[0].Body != "two" {
			t.Fatalf("Since = %v, want just the second message", got)
		}
		if got[0].Author != "Ada" {
			t.Errorf("Author = %q, want Ada — the name is joined on read", got[0].Author)
		}
	})

	t.Run("Recent brings back the newest, oldest first", func(t *testing.T) {
		got, err := messages.Recent(t.Context(), general.ID, 1)
		if err != nil {
			t.Fatalf("Recent: %v", err)
		}
		if len(got) != 1 || got[0].Body != "two" {
			t.Errorf("Recent(1) = %v, want the newest message", got)
		}

		all, err := messages.Recent(t.Context(), general.ID, 10)
		if err != nil {
			t.Fatalf("Recent: %v", err)
		}
		if len(all) != 2 || all[0].Body != "one" {
			t.Errorf("Recent(10) = %v, want both, oldest first", all)
		}
	})

	t.Run("the time survives the round trip", func(t *testing.T) {
		got, _ := messages.Recent(t.Context(), general.ID, 1)
		if got[0].CreatedAt.IsZero() {
			t.Error("CreatedAt came back zero")
		}
	})
}

func TestSessions(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessions := store.NewSessions(db)

	t.Run("what is committed is found", func(t *testing.T) {
		if err := sessions.CommitCtx(t.Context(), "token-1", []byte("data"), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		got, found, err := sessions.FindCtx(t.Context(), "token-1")
		if err != nil || !found || string(got) != "data" {
			t.Errorf("FindCtx = %q, %v, %v; want data, true, nil", got, found, err)
		}
	})

	t.Run("committing the same token again replaces it", func(t *testing.T) {
		if err := sessions.CommitCtx(t.Context(), "token-1", []byte("newer"), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		got, _, _ := sessions.FindCtx(t.Context(), "token-1")
		if string(got) != "newer" {
			t.Errorf("FindCtx = %q, want newer", got)
		}
	})

	// This is the one that matters. scs performs no expiry check of its own, so
	// a store that returns expired rows keeps those sessions alive and every
	// request refreshes their idle deadline — IdleTimeout silently off.
	t.Run("an expired session is not found, before any janitor runs", func(t *testing.T) {
		if err := sessions.CommitCtx(t.Context(), "old", []byte("data"), time.Now().Add(-time.Second)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		_, found, err := sessions.FindCtx(t.Context(), "old")
		if err != nil || found {
			t.Errorf("FindCtx found an expired session (%v)", err)
		}
	})

	t.Run("delete removes it", func(t *testing.T) {
		if err := sessions.DeleteCtx(t.Context(), "token-1"); err != nil {
			t.Fatalf("DeleteCtx: %v", err)
		}
		if _, found, _ := sessions.FindCtx(t.Context(), "token-1"); found {
			t.Error("the session survived a delete")
		}
	})

	t.Run("the janitor reclaims expired rows", func(t *testing.T) {
		if err := sessions.DeleteExpired(t.Context()); err != nil {
			t.Fatalf("DeleteExpired: %v", err)
		}
		var n int
		if err := db.Read.QueryRowContext(t.Context(), "SELECT count(*) FROM sessions").Scan(&n); err != nil {
			t.Fatalf("counting sessions: %v", err)
		}
		if n != 0 {
			t.Errorf("%d session rows left, want 0", n)
		}
	})
}

func TestTokens(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	tokens := store.NewTokens(db)
	ada := addUser(t, db, "Ada")
	bob := addUser(t, db, "Bob")

	token := &domain.Token{ID: "t-1", UserID: ada.ID, Hash: "hash-1", Label: "laptop"}
	if err := tokens.Add(t.Context(), token); err != nil {
		t.Fatalf("Add: %v", err)
	}

	t.Run("the hash finds its owner", func(t *testing.T) {
		got, err := tokens.UserByHash(t.Context(), "hash-1")
		if err != nil || got.ID != ada.ID {
			t.Errorf("UserByHash = %v, %v; want Ada", got, err)
		}
	})

	t.Run("an unknown hash finds nobody", func(t *testing.T) {
		_, err := tokens.UserByHash(t.Context(), "nothing")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("using it records when", func(t *testing.T) {
		got, err := tokens.ByUser(t.Context(), ada.ID)
		if err != nil || len(got) != 1 {
			t.Fatalf("ByUser = %v, %v", got, err)
		}
		if got[0].LastUsedAt == "" {
			t.Error("LastUsedAt is empty after the token was used")
		}
	})

	t.Run("somebody else's token cannot be revoked", func(t *testing.T) {
		err := tokens.Delete(t.Context(), bob.ID, "t-1")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if _, err := tokens.UserByHash(t.Context(), "hash-1"); err != nil {
			t.Errorf("the token stopped working after somebody else tried to revoke it: %v", err)
		}
	})

	t.Run("its owner can", func(t *testing.T) {
		if err := tokens.Delete(t.Context(), ada.ID, "t-1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := tokens.UserByHash(t.Context(), "hash-1"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

// TestForeignKeysAreEnforced proves the pragma is on: without it SQLite accepts
// a message pointing at a room that does not exist.
func TestForeignKeysAreEnforced(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	messages := store.NewMessages(db)
	ada := addUser(t, db, "Ada")

	err := messages.Add(t.Context(), &domain.Message{
		RoomID: "no-such-room", AuthorID: ada.ID, Body: "hello",
	})
	if err == nil {
		t.Error("a message was stored in a room that does not exist")
	}
}

// TestMigrationsAreIdempotent covers the boot path: opening a database twice
// must not try to create the tables again.
func TestMigrationsAreIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "twice.db")

	first, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	first.Close()

	second, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	second.Close()
}

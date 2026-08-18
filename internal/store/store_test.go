package store_test

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
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
		if err := messages.Add(t.Context(), m, nil); err != nil {
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

	t.Run("Page with no cursor brings back the newest, oldest first", func(t *testing.T) {
		got, err := messages.Page(t.Context(), general.ID, 0, 1)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		if len(got) != 1 || got[0].Body != "two" {
			t.Errorf("Page(0, 1) = %v, want the newest message", got)
		}

		all, err := messages.Page(t.Context(), general.ID, 0, 10)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		if len(all) != 2 || all[0].Body != "one" {
			t.Errorf("Page(0, 10) = %v, want both, oldest first", all)
		}
	})

	t.Run("Page walks backwards from a cursor", func(t *testing.T) {
		// The keyset read: everything strictly older than the cursor, and
		// nothing from another room.
		got, err := messages.Page(t.Context(), general.ID, second.Seq, 10)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		if len(got) != 1 || got[0].Body != "one" {
			t.Fatalf("Page(second, 10) = %v, want only the first message", got)
		}

		// Past the beginning there is nothing left, which is how the page
		// renders no "Show older" control rather than an empty one.
		none, err := messages.Page(t.Context(), general.ID, first.Seq, 10)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		if len(none) != 0 {
			t.Errorf("Page(first, 10) = %v, want nothing older", none)
		}
	})

	t.Run("the time survives the round trip", func(t *testing.T) {
		got, _ := messages.Page(t.Context(), general.ID, 0, 1)
		if got[0].CreatedAt.IsZero() {
			t.Error("CreatedAt came back zero")
		}
	})

	t.Run("a message with an attachment reads back with it", func(t *testing.T) {
		withFile := &domain.Message{RoomID: general.ID, AuthorID: ada.ID, Body: "look"}
		withFile.Attachment = &domain.Attachment{
			ID: "att-1", UploaderID: ada.ID, Name: "note.txt",
			Kind: "text/plain; charset=utf-8", Size: 5,
		}
		if err := messages.Add(t.Context(), withFile, []byte("hello")); err != nil {
			t.Fatalf("adding a message with a file: %v", err)
		}
		got, err := messages.Page(t.Context(), general.ID, 0, 1)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		// The LEFT JOIN is the thing under test: one read brings the file back
		// with the message, and messages without one still come back.
		if len(got) != 1 || got[0].Attachment == nil {
			t.Fatalf("Page = %v, want the newest message carrying its attachment", got)
		}
		if got[0].Attachment.Name != "note.txt" || got[0].Attachment.Size != 5 {
			t.Errorf("attachment = %+v, want note.txt of 5 bytes", got[0].Attachment)
		}

		plain, err := messages.Page(t.Context(), general.ID, withFile.Seq, 1)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		if len(plain) != 1 || plain[0].Attachment != nil {
			t.Errorf("older message = %v, want no attachment", plain)
		}
	})
}

func TestAttachments(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	messages := store.NewMessages(db)
	attachments := store.NewAttachments(db)
	ada := addUser(t, db, "Ada")
	bob := addUser(t, db, "Bob")
	general := addRoom(t, db, "General")

	post := func(id, uploader string) {
		t.Helper()
		m := &domain.Message{RoomID: general.ID, AuthorID: uploader, Body: "here"}
		m.Attachment = &domain.Attachment{
			ID: id, UploaderID: uploader, Name: id + ".png", Kind: "image/png", Size: 4,
		}
		if err := messages.Add(t.Context(), m, []byte("\x89PNG")); err != nil {
			t.Fatalf("adding a message with a file: %v", err)
		}
	}
	post("ada-file", ada.ID)

	t.Run("Open brings back the bytes and the sniffed type", func(t *testing.T) {
		file, content, err := attachments.Open(t.Context(), "ada-file")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer content.Close()
		if file.Kind != "image/png" {
			t.Errorf("Kind = %q, want image/png", file.Kind)
		}
		bs, err := io.ReadAll(content)
		if err != nil {
			t.Fatalf("reading the file: %v", err)
		}
		if string(bs) != "\x89PNG" {
			t.Errorf("bytes = %q, want the bytes that went in", bs)
		}
	})

	t.Run("Open answers ErrNotFound for a file that is not there", func(t *testing.T) {
		if _, _, err := attachments.Open(t.Context(), "nobody"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Open(nobody) = %v, want ErrNotFound", err)
		}
	})

	t.Run("somebody else's file cannot be deleted, and answers like one that is gone", func(t *testing.T) {
		// The two-user test at the store: the ownership predicate is in the
		// WHERE clause, so Bob's delete matches no row and says so exactly the
		// way a missing file does.
		if err := attachments.Delete(t.Context(), bob.ID, "ada-file"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Bob deleting Ada's file = %v, want ErrNotFound", err)
		}
		if _, content, err := attachments.Open(t.Context(), "ada-file"); err != nil {
			t.Errorf("Ada's file after Bob's delete: %v, want it still there", err)
		} else {
			content.Close()
		}
		if err := attachments.Delete(t.Context(), ada.ID, "ada-file"); err != nil {
			t.Fatalf("Ada deleting her own file: %v", err)
		}
		if _, _, err := attachments.Open(t.Context(), "ada-file"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("the file after Ada's delete = %v, want ErrNotFound", err)
		}
	})
}

func TestResetsAndOutbox(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	resets := store.NewResets(db)
	outbox := store.NewOutbox(db)
	ada := addUser(t, db, "Ada")

	mail := domain.Mail{To: "ada@example.com", Subject: "Reset", Text: "https://example.com/reset?t=x"}
	res := &domain.Reset{Hash: "hash-1", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := resets.Add(t.Context(), res, mail); err != nil {
		t.Fatalf("Add: %v", err)
	}

	t.Run("the link and its message are written together", func(t *testing.T) {
		queued, err := outbox.Unsent(t.Context(), 10)
		if err != nil {
			t.Fatalf("Unsent: %v", err)
		}
		if len(queued) != 1 || queued[0].Mail.To != "ada@example.com" {
			t.Fatalf("Unsent = %v, want the one message the reset queued", queued)
		}
		if err := outbox.Sent(t.Context(), queued[0].ID); err != nil {
			t.Fatalf("Sent: %v", err)
		}
		again, err := outbox.Unsent(t.Context(), 10)
		if err != nil {
			t.Fatalf("Unsent: %v", err)
		}
		if len(again) != 0 {
			t.Errorf("Unsent after Sent = %v, want nothing left", again)
		}
	})

	t.Run("a header with a line break is refused before anything is written", func(t *testing.T) {
		bad := domain.Mail{To: "ada@example.com\r\nBcc: attacker@example.com", Subject: "Reset"}
		err := resets.Add(t.Context(), &domain.Reset{
			Hash: "hash-2", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(time.Hour),
		}, bad)
		if !errors.Is(err, domain.ErrBadHeader) {
			t.Fatalf("Add with a line break in To = %v, want ErrBadHeader", err)
		}
		if _, err := resets.Take(t.Context(), "hash-2", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("the reset was stored anyway: %v", err)
		}
	})

	t.Run("a link works once", func(t *testing.T) {
		got, err := resets.Take(t.Context(), "hash-1", time.Now().UTC())
		if err != nil {
			t.Fatalf("Take: %v", err)
		}
		if got.UserID != ada.ID {
			t.Errorf("UserID = %q, want %q", got.UserID, ada.ID)
		}
		if _, err := resets.Take(t.Context(), "hash-1", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("second Take = %v, want ErrNotFound", err)
		}
	})

	t.Run("an expired link is spent and refused", func(t *testing.T) {
		old := &domain.Reset{Hash: "hash-3", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(-time.Minute)}
		if err := resets.Add(t.Context(), old, mail); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if _, err := resets.Take(t.Context(), "hash-3", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Take of an expired link = %v, want ErrNotFound", err)
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
	}, nil)
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

// TestDeletingAUserLeavesNoRowBehind is the test patterns/go-data-deletion.md
// asks every project for, and the reason it reads the schema at runtime is that
// the defect it hunts does not exist yet: a table added next year with a
// user_id column and no REFERENCES clause. The delete would still succeed, the
// rows would still be there, and a test naming today's tables would still be
// green. This one fails the day that table is created with a row in it.
func TestDeletingAUserLeavesNoRowBehind(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ada := addUser(t, db, "Ada")
	bob := addUser(t, db, "Bob")
	general := addRoom(t, db, "General")

	// One row for Ada in every table that can hold one. A test that deletes an
	// account with nothing attached proves that empty tables are empty.
	messages := store.NewMessages(db)
	m := &domain.Message{RoomID: general.ID, AuthorID: ada.ID, Body: "here"}
	m.Attachment = &domain.Attachment{
		ID: "ada-file", UploaderID: ada.ID, Name: "ada.png", Kind: "image/png", Size: 4,
	}
	if err := messages.Add(t.Context(), m, []byte("\x89PNG")); err != nil {
		t.Fatalf("adding a message with a file: %v", err)
	}
	if err := store.NewTokens(db).Add(t.Context(), &domain.Token{
		ID: "ada-token", UserID: ada.ID, Hash: "token-hash", Label: "laptop",
	}); err != nil {
		t.Fatalf("adding a token: %v", err)
	}
	if err := store.NewResets(db).Add(t.Context(),
		&domain.Reset{Hash: "ada-reset", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(time.Hour)},
		domain.Mail{To: "ada@example.com", Subject: "Reset", Text: "https://example.com/reset?t=x"},
	); err != nil {
		t.Fatalf("adding a reset: %v", err)
	}

	// Bob gets one of everything too, so the sweep below proves the cascade is
	// aimed rather than indiscriminate.
	bobMsg := &domain.Message{RoomID: general.ID, AuthorID: bob.ID, Body: "still here"}
	if err := messages.Add(t.Context(), bobMsg, nil); err != nil {
		t.Fatalf("adding bob's message: %v", err)
	}

	if err := store.NewUsers(db).Delete(t.Context(), ada.ID); err != nil {
		t.Fatalf("deleting Ada: %v", err)
	}

	for _, table := range tableNames(t, db) {
		for _, col := range textColumns(t, db, table) {
			var n int
			// Identifiers cannot be bound. These come from sqlite_master, never
			// from anything a request touched.
			q := fmt.Sprintf(`SELECT count(*) FROM %q WHERE %q = ?`, table, col)
			if err := db.Read.QueryRow(q, ada.ID).Scan(&n); err != nil {
				t.Fatalf("counting %s.%s: %v", table, col, err)
			}
			if n != 0 {
				t.Errorf("%s.%s still holds the deleted user in %d row(s)", table, col, n)
			}
		}
	}

	// The sweep above finds rows that name Ada by id. It cannot find a row that
	// holds her data under another name, and the outbox is exactly that: it
	// stores an address, and an address is not an id. Removing the user_id this
	// release added leaves the sweep green and the address on disk — so the
	// column a delete follows needs an assertion of its own.
	var queued int
	if err := db.Read.QueryRow(
		`SELECT count(*) FROM outbox WHERE recipient = ?`, "ada@example.com").Scan(&queued); err != nil {
		t.Fatalf("counting queued mail: %v", err)
	}
	if queued != 0 {
		t.Errorf("%d queued message(s) still address the deleted account", queued)
	}

	// The one place neither check can look: a session names its user inside an
	// opaque payload, not in a column. What ends those is authenticate
	// resolving the credential to a row — TestDeletingAnAccountSignsTheBrowserOut.
	var left int
	if err := db.Read.QueryRow(`SELECT count(*) FROM messages WHERE author_id = ?`, bob.ID).
		Scan(&left); err != nil {
		t.Fatalf("counting bob's messages: %v", err)
	}
	if left != 1 {
		t.Errorf("bob has %d messages left, want the 1 he wrote", left)
	}
}

// tableNames lists every table the app owns, read from the schema so a table
// added later is covered without anybody remembering to add it here.
func tableNames(t *testing.T, db *store.DB) []string {
	t.Helper()
	rows, err := db.Read.Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scanning a table name: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading table names: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no tables: the sweep below would pass without looking at anything")
	}
	return names
}

// textColumns lists a table's TEXT columns, which is where a user id can be.
func textColumns(t *testing.T, db *store.DB, table string) []string {
	t.Helper()
	rows, err := db.Read.Query(fmt.Sprintf(`SELECT name, type FROM pragma_table_info(%q)`, table))
	if err != nil {
		t.Fatalf("reading the columns of %s: %v", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			t.Fatalf("scanning a column of %s: %v", table, err)
		}
		if strings.EqualFold(kind, "TEXT") {
			cols = append(cols, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the columns of %s: %v", table, err)
	}
	return cols
}

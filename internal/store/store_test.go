package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andygeiss/baseline-reference/v6/internal/domain"
	"github.com/andygeiss/baseline-reference/v6/internal/store"
	"modernc.org/sqlite"
)

// newTestDB opens a real database in a temporary directory, with the production
// pragmas and migrations. The SQL is the unit under test here, so there is
// nothing to fake.
//
// It also fails the test that leaves a pool in use, and for that it has to be
// called by the test or subtest that uses the database, never above a run of
// subtests: a Rows leaked under a subtest's t.Context() is released soon after
// that subtest ends, and a check registered above it mostly finds the pool idle
// (patterns/go-sqlite.md, Testing). Measured here, under -race: the check above
// a leaking subtest saw the leaked Rows in 7 runs of 40, and the leaking
// subtest's own check in 40 of 40. So a test here either uses the database
// itself, in one function with no subtests under it, or gives each subtest a
// database of its own.
func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// Registered after the Close cleanup, so LIFO runs this one first. A leaked
	// Rows or Tx wedges the one-connection write pool, and neither go vet nor
	// staticcheck reports it (patterns/go-sqlite.md rule 8). Both pools: a
	// SELECT runs on the read pool, so a check of the write pool alone passes
	// a leaked Rows.
	t.Cleanup(func() {
		for name, pool := range map[string]*sql.DB{"read": db.Read, "write": db.Write} {
			if n := pool.Stats().InUse; n != 0 {
				t.Errorf("%s pool: %d connection(s) still in use — a Rows or Tx was not closed", name, n)
			}
		}
	})
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

	// Found by name, ignoring capitals.
	{
		got, err := users.ByName(t.Context(), "ADA")
		if err != nil {
			t.Fatalf("ByName: %v", err)
		}
		if got.ID != ada.ID {
			t.Errorf("ID = %q, want %q", got.ID, ada.ID)
		}
	}

	// Found by ID.
	{
		got, err := users.ByID(t.Context(), ada.ID)
		if err != nil || got.Name != "Ada" {
			t.Errorf("ByID = %v, %v; want Ada", got, err)
		}
	}

	// An unknown name is not found.
	{
		_, err := users.ByName(t.Context(), "Nobody")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	}

	// A name differing only by capitals is taken.
	{
		err := users.Add(t.Context(), &domain.User{ID: "other", Name: "ada", PasswordHash: "hash"})
		if !errors.Is(err, domain.ErrNameTaken) {
			t.Errorf("err = %v, want ErrNameTaken", err)
		}
	}

	// A new account starts its password counter at zero.
	{
		got, err := users.ByID(t.Context(), ada.ID)
		if err != nil || got.PasswordVersion != 0 {
			t.Errorf("PasswordVersion = %d (%v), want 0", got.PasswordVersion, err)
		}
	}
}

// An error's text is logged by whoever handles it, so it names a person by id
// and never by name (patterns/go-errors-logging.md, Never log).
func TestAddingAUserNamesNobodyInItsError(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	gone, cancel := context.WithCancel(t.Context())
	cancel()

	err := store.NewUsers(db).Add(gone, &domain.User{ID: "user-1", Name: "Ada Lovelace", PasswordHash: "hash"})

	if err == nil {
		t.Fatal("Add under a cancelled context = nil, want the failure")
	}
	if strings.Contains(err.Error(), "Ada") || strings.Contains(err.Error(), "Lovelace") {
		t.Errorf("the error names the person: %v", err)
	}
	if !strings.Contains(err.Error(), "user-1") {
		t.Errorf("the error does not say which user, by id: %v", err)
	}
}

// resetPassword sets a new password for a user the way the app does, through a
// reset link, and so moves the password counter by one.
func resetPassword(t *testing.T, db *store.DB, userID, hash string) {
	t.Helper()
	link := "link-for-" + hash
	addReset(t, db, link, userID, time.Hour)
	if _, err := store.NewResets(db).Redeem(t.Context(), link, hash, time.Now().UTC()); err != nil {
		t.Fatalf("resetting the password of %s: %v", userID, err)
	}
}

// A rehash sets no password, so it moves no counter; a password allowed it, so
// it names the counter its request loaded, and no row changed is a refusal
// (patterns/go-auth-sessions.md rule 1).
func TestRehash(t *testing.T) {
	t.Parallel()

	t.Run("it writes the hash and leaves the counter where it is", func(t *testing.T) {
		db := newTestDB(t)
		users := store.NewUsers(db)
		ada := addUser(t, db, "Ada")
		resetPassword(t, db, ada.ID, "weak") // so the counter is not the zero a new row has

		if err := users.Rehash(t.Context(), ada.ID, "stronger", 1); err != nil {
			t.Fatalf("Rehash: %v", err)
		}
		got, err := users.ByID(t.Context(), ada.ID)
		if err != nil || got.PasswordHash != "stronger" || got.PasswordVersion != 1 {
			t.Errorf("row = %q at %d (%v), want stronger at 1", got.PasswordHash, got.PasswordVersion, err)
		}
	})

	// The sign-in that loaded version 0 writes after a reset moved the
	// counter: its hash is of the password the reset locked out, and must not
	// land.
	t.Run("a write that names a value the row no longer holds changes nothing", func(t *testing.T) {
		db := newTestDB(t)
		users := store.NewUsers(db)
		ada := addUser(t, db, "Ada")
		resetPassword(t, db, ada.ID, "the-reset")

		if err := users.Rehash(t.Context(), ada.ID, "the-thief", 0); !errors.Is(err, domain.ErrStale) {
			t.Fatalf("the late write = %v, want ErrStale", err)
		}
		got, err := users.ByID(t.Context(), ada.ID)
		if err != nil || got.PasswordHash != "the-reset" || got.PasswordVersion != 1 {
			t.Errorf("row = %q at %d (%v), want the-reset at 1", got.PasswordHash, got.PasswordVersion, err)
		}
	})

	// A sign-in does not spend a reset link, and a rehash is part of a
	// sign-in: only setting a password deletes the user's links.
	t.Run("it leaves the reset links alone", func(t *testing.T) {
		db := newTestDB(t)
		users, resets := store.NewUsers(db), store.NewResets(db)
		ada := addUser(t, db, "Ada")
		addReset(t, db, "adas-link", ada.ID, time.Hour)

		if err := users.Rehash(t.Context(), ada.ID, "stronger", 0); err != nil {
			t.Fatalf("Rehash: %v", err)
		}
		if _, err := resets.Redeem(t.Context(), "adas-link", "reset", time.Now().UTC()); err != nil {
			t.Errorf("a rehash deleted the link: %v", err)
		}
	})
}

// Making a machine token is a statement a password allowed, so its INSERT
// names the password counter its request loaded: no row inserted is a refusal
// (patterns/go-auth-sessions.md rule 1).
func TestATokenIsAddedOnlyAtTheCounterItsRequestLoaded(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	tokens := store.NewTokens(db)
	ada := addUser(t, db, "Ada")
	resetPassword(t, db, ada.ID, "the-reset") // the counter moves from 0 to 1

	// The request that loaded the row before the reset.
	err := tokens.Add(t.Context(), &domain.Token{ID: "t-held", UserID: ada.ID, Hash: "hash-held", Label: "held"}, 0)
	if !errors.Is(err, domain.ErrStale) {
		t.Errorf("Add at the counter the row no longer holds = %v, want ErrStale", err)
	}
	if listed, err := tokens.ByUser(t.Context(), ada.ID); err != nil || len(listed) != 0 {
		t.Errorf("%d token(s) after the refused insert (%v), want none", len(listed), err)
	}

	// The request that loaded it after.
	if err := tokens.Add(t.Context(), &domain.Token{ID: "t-now", UserID: ada.ID, Hash: "hash-now", Label: "now"}, 1); err != nil {
		t.Fatalf("Add at the current counter: %v", err)
	}
	if listed, err := tokens.ByUser(t.Context(), ada.ID); err != nil || len(listed) != 1 {
		t.Errorf("%d token(s) after the one good insert (%v), want 1", len(listed), err)
	}

	// A request signed by that token loads the same row a session's does, the
	// counter with it: it may go on to a statement that names the counter.
	if err := store.NewUsers(db).SetEmail(t.Context(), ada.ID, "ada@example.com"); err != nil {
		t.Fatalf("storing Ada's address: %v", err)
	}
	for name, lookup := range map[string]func(context.Context, string) (domain.User, error){
		"Owner": tokens.Owner, "UserByHash": tokens.UserByHash,
	} {
		got, err := lookup(t.Context(), "hash-now")
		if err != nil || got.PasswordVersion != 1 || got.Email != "ada@example.com" || got.PasswordHash != "the-reset" {
			t.Errorf("%s = %q at %d, %q (%v); want the-reset at 1, ada@example.com",
				name, got.PasswordHash, got.PasswordVersion, got.Email, err)
		}
	}
}

// Deleting an account is a statement a password allowed, so its DELETE names
// the counter its request loaded (patterns/go-data-deletion.md, Deleting is a
// write).
func TestAnAccountIsDeletedOnlyAtTheCounterItsRequestLoaded(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	users := store.NewUsers(db)
	ada := addUser(t, db, "Ada")
	if err := users.SetEmail(t.Context(), ada.ID, "ada@example.com"); err != nil {
		t.Fatalf("storing Ada's address: %v", err)
	}
	// Mail queued by address alone, which the delete removes by hand before
	// it reaches the account: a refused delete must put it back.
	if _, err := db.Write.ExecContext(t.Context(),
		`INSERT INTO outbox (id, recipient, subject, body, created_at) VALUES ('to-ada', 'ada@example.com', 'Invitation', 'link', ?)`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("queueing mail with no user: %v", err)
	}
	resetPassword(t, db, ada.ID, "the-reset") // the counter moves from 0 to 1

	// The request that loaded the row before the reset.
	if err := users.Delete(t.Context(), ada.ID, 0); !errors.Is(err, domain.ErrStale) {
		t.Fatalf("Delete at the counter the row no longer holds = %v, want ErrStale", err)
	}
	if got, err := users.ByID(t.Context(), ada.ID); err != nil || got.PasswordHash != "the-reset" {
		t.Fatalf("Ada after the refused delete = %q (%v), want her row as the reset left it", got.PasswordHash, err)
	}
	var queued int
	if err := db.Read.QueryRowContext(t.Context(),
		`SELECT count(*) FROM outbox WHERE id = 'to-ada'`).Scan(&queued); err != nil || queued != 1 {
		t.Errorf("%d row(s) of mail queued by address after the refused delete (%v), want 1", queued, err)
	}

	// The request that loaded it after, and then one for an account that is
	// already gone: that is not a refusal.
	if err := users.Delete(t.Context(), ada.ID, 1); err != nil {
		t.Fatalf("Delete at the current counter: %v", err)
	}
	if _, err := users.ByID(t.Context(), ada.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Ada after the delete: err = %v, want ErrNotFound", err)
	}
	if err := users.Delete(t.Context(), ada.ID, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete of an account that is gone = %v, want ErrNotFound", err)
	}
}

// addReset stores one reset link for a user, with the mail that carries it.
func addReset(t *testing.T, db *store.DB, hash, userID string, lifetime time.Duration) {
	t.Helper()
	res := &domain.Reset{Hash: hash, UserID: userID, ExpiresAt: time.Now().UTC().Add(lifetime)}
	mail := domain.Mail{To: "someone@example.com", Subject: "Reset", Text: "https://example.com/reset?t=x"}
	if err := store.NewResets(db).Add(t.Context(), res, mail); err != nil {
		t.Fatalf("adding reset %s: %v", hash, err)
	}
}

// A reset link is spent in the transaction that writes the new hash, with every
// other link the user has (patterns/go-auth-sessions.md, Password reset).
func TestRedeem(t *testing.T) {
	t.Parallel()

	// Machine tokens rule 5: whoever knew the old password may have made a
	// token, and the reset is what ends it.
	t.Run("it deletes the user's machine tokens, and nobody else's", func(t *testing.T) {
		db := newTestDB(t)
		ada, bob := addUser(t, db, "Ada"), addUser(t, db, "Bob")
		tokens := store.NewTokens(db)
		for _, tok := range []domain.Token{
			{ID: "ada-laptop", UserID: ada.ID, Hash: "ada-hash-1", Label: "laptop"},
			{ID: "ada-script", UserID: ada.ID, Hash: "ada-hash-2", Label: "script"},
			{ID: "bob-laptop", UserID: bob.ID, Hash: "bob-hash", Label: "laptop"},
		} {
			if err := tokens.Add(t.Context(), &tok, 0); err != nil {
				t.Fatalf("adding token %s: %v", tok.ID, err)
			}
		}
		addReset(t, db, "link", ada.ID, time.Hour)

		if _, err := store.NewResets(db).Redeem(t.Context(), "link", "new-hash", time.Now().UTC()); err != nil {
			t.Fatalf("Redeem: %v", err)
		}

		for _, hash := range []string{"ada-hash-1", "ada-hash-2"} {
			if _, err := tokens.Owner(t.Context(), hash); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("Owner(%s) after the reset = %v, want ErrNotFound: the token outlived the password", hash, err)
			}
		}
		if left, err := tokens.ByUser(t.Context(), ada.ID); err != nil || len(left) != 0 {
			t.Errorf("Ada still lists %d tokens (%v), want none", len(left), err)
		}
		if u, err := tokens.Owner(t.Context(), "bob-hash"); err != nil || u.ID != bob.ID {
			t.Errorf("Owner(bob-hash) = %q, %v; want Bob: somebody else's reset deleted his token", u.ID, err)
		}
	})

	t.Run("a link that is refused deletes no token", func(t *testing.T) {
		db := newTestDB(t)
		ada := addUser(t, db, "Ada")
		tokens := store.NewTokens(db)
		if err := tokens.Add(t.Context(), &domain.Token{ID: "ada-laptop", UserID: ada.ID, Hash: "ada-hash", Label: "laptop"}, 0); err != nil {
			t.Fatalf("adding a token: %v", err)
		}
		addReset(t, db, "old", ada.ID, -time.Minute)

		for _, link := range []string{"old", "guessed"} {
			if _, err := store.NewResets(db).Redeem(t.Context(), link, "x", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("Redeem(%s) = %v, want ErrNotFound", link, err)
			}
		}
		if u, err := tokens.Owner(t.Context(), "ada-hash"); err != nil || u.ID != ada.ID {
			t.Errorf("Owner(ada-hash) = %q, %v; want Ada: a dead link deleted her token", u.ID, err)
		}
	})

	t.Run("it sets the password, moves the counter, and returns the row as it now stands", func(t *testing.T) {
		db := newTestDB(t)
		ada := addUser(t, db, "Ada")
		addReset(t, db, "link", ada.ID, time.Hour)

		got, err := store.NewResets(db).Redeem(t.Context(), "link", "new-hash", time.Now().UTC())
		if err != nil {
			t.Fatalf("Redeem: %v", err)
		}
		if got.ID != ada.ID || got.PasswordHash != "new-hash" || got.PasswordVersion != 1 {
			t.Errorf("Redeem returned %q, %q at %d; want Ada's row, new-hash at 1", got.ID, got.PasswordHash, got.PasswordVersion)
		}
		stored, err := store.NewUsers(db).ByID(t.Context(), ada.ID)
		if err != nil || stored.PasswordHash != "new-hash" || stored.PasswordVersion != 1 {
			t.Errorf("stored row = %q at %d (%v), want new-hash at 1", stored.PasswordHash, stored.PasswordVersion, err)
		}
	})

	t.Run("a link works once", func(t *testing.T) {
		db := newTestDB(t)
		ada := addUser(t, db, "Ada")
		addReset(t, db, "link", ada.ID, time.Hour)
		resets := store.NewResets(db)

		if _, err := resets.Redeem(t.Context(), "link", "first", time.Now().UTC()); err != nil {
			t.Fatalf("Redeem: %v", err)
		}
		if _, err := resets.Redeem(t.Context(), "link", "second", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("second Redeem = %v, want ErrNotFound", err)
		}
		if got, _ := store.NewUsers(db).ByID(t.Context(), ada.ID); got.PasswordHash != "first" || got.PasswordVersion != 1 {
			t.Errorf("row = %q at %d, want first at 1", got.PasswordHash, got.PasswordVersion)
		}
	})

	// Two links asked for, one used: the other was issued before this reset
	// and must not set the password after it.
	t.Run("it takes the user's other links with it", func(t *testing.T) {
		db := newTestDB(t)
		ada, bob := addUser(t, db, "Ada"), addUser(t, db, "Bob")
		addReset(t, db, "first-link", ada.ID, time.Hour)
		addReset(t, db, "second-link", ada.ID, time.Hour)
		addReset(t, db, "bobs-link", bob.ID, time.Hour)
		resets := store.NewResets(db)

		if _, err := resets.Redeem(t.Context(), "second-link", "mine", time.Now().UTC()); err != nil {
			t.Fatalf("Redeem: %v", err)
		}
		if _, err := resets.Redeem(t.Context(), "first-link", "the-older-link", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("the link issued before the reset still redeems: %v", err)
		}
		if got, _ := store.NewUsers(db).ByID(t.Context(), ada.ID); got.PasswordHash != "mine" {
			t.Errorf("hash = %q, want the one the reset set", got.PasswordHash)
		}
		if _, err := resets.Redeem(t.Context(), "bobs-link", "bobs", time.Now().UTC()); err != nil {
			t.Errorf("somebody else's link went with it: %v", err)
		}
	})

	t.Run("an expired link is spent and refused, and changes no password", func(t *testing.T) {
		db := newTestDB(t)
		ada := addUser(t, db, "Ada")
		addReset(t, db, "old", ada.ID, -time.Minute)
		resets := store.NewResets(db)

		if _, err := resets.Redeem(t.Context(), "old", "too-late", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Redeem of an expired link = %v, want ErrNotFound", err)
		}
		if got, _ := store.NewUsers(db).ByID(t.Context(), ada.ID); got.PasswordHash != "hash" || got.PasswordVersion != 0 {
			t.Errorf("row = %q at %d, want it untouched", got.PasswordHash, got.PasswordVersion)
		}
		var n int
		if err := db.Read.QueryRowContext(t.Context(), "SELECT count(*) FROM resets").Scan(&n); err != nil || n != 0 {
			t.Errorf("%d reset row(s) left (%v), want the expired one deleted", n, err)
		}
	})

	t.Run("a link nobody was sent is refused, and changes no password", func(t *testing.T) {
		db := newTestDB(t)
		ada := addUser(t, db, "Ada")

		if _, err := store.NewResets(db).Redeem(t.Context(), "guessed", "x", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Redeem of an unknown link = %v, want ErrNotFound", err)
		}
		if got, _ := store.NewUsers(db).ByID(t.Context(), ada.ID); got.PasswordHash != "hash" || got.PasswordVersion != 0 {
			t.Errorf("row = %q at %d, want it untouched", got.PasswordHash, got.PasswordVersion)
		}
	})
}

func TestRooms(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	rooms := store.NewRooms(db)
	addRoom(t, db, "General")

	// Found by slug.
	{
		got, err := rooms.BySlug(t.Context(), "general")
		if err != nil || got.Name != "General" {
			t.Errorf("BySlug = %v, %v; want General", got, err)
		}
	}

	// An unknown slug is not found.
	{
		_, err := rooms.BySlug(t.Context(), "nowhere")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	}

	// The same address twice is refused.
	{
		err := rooms.Add(t.Context(), &domain.Room{ID: "other", Slug: "general", Name: "general"})
		if !errors.Is(err, domain.ErrSlugTaken) {
			t.Errorf("err = %v, want ErrSlugTaken", err)
		}
	}

	// Listed by name.
	{
		addRoom(t, db, "Announcements")
		got, err := rooms.All(t.Context())
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		if len(got) != 2 || got[0].Name != "Announcements" {
			t.Errorf("All = %v, want Announcements first", got)
		}
	}
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

	// The sequence comes back on the message.
	{
		// The caller needs it: that number is the cursor every reader polls with.
		if first.Seq == 0 || second.Seq <= first.Seq {
			t.Errorf("sequences = %d, %d; want the second to be higher and neither zero",
				first.Seq, second.Seq)
		}
	}

	// Since brings back only what is newer, in one room.
	{
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
	}

	// Page with no cursor brings back the newest, oldest first.
	{
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
	}

	// Page walks backwards from a cursor.
	{
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
	}

	// The time survives the round trip.
	{
		got, _ := messages.Page(t.Context(), general.ID, 0, 1)
		if got[0].CreatedAt.IsZero() {
			t.Error("CreatedAt came back zero")
		}
	}

	// A message with an attachment reads back with it.
	{
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
	}
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

	// Open brings back the bytes and the sniffed type.
	{
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
	}

	// Open answers ErrNotFound for a file that is not there.
	{
		if _, _, err := attachments.Open(t.Context(), "nobody"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Open(nobody) = %v, want ErrNotFound", err)
		}
	}

	// Somebody else's file cannot be deleted, and answers like one that is gone.
	{
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
	}
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

	// The link and its message are written together.
	{
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
		// The body held the link, and the resets table keeps only its hash:
		// a sent row that kept its body would keep the plaintext.
		if body := outboxBody(t, db, queued[0].ID); body != "" {
			t.Errorf("the sent row still holds its body: %q", body)
		}
	}

	// A message that is given up on loses its body with the last attempt.
	{
		res := &domain.Reset{Hash: "hash-failing", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		if err := resets.Add(t.Context(), res, mail); err != nil {
			t.Fatalf("Add: %v", err)
		}
		queued, err := outbox.Unsent(t.Context(), 10)
		if err != nil || len(queued) != 1 {
			t.Fatalf("Unsent = %v, %v, want the one new message", queued, err)
		}
		id := queued[0].ID
		for range store.MaxSendAttempts - 1 {
			if err := outbox.Failed(t.Context(), id); err != nil {
				t.Fatalf("Failed: %v", err)
			}
		}
		// One attempt left: the next tick still has to send it.
		if body := outboxBody(t, db, id); body != mail.Text {
			t.Errorf("body before the last attempt = %q, want it intact", body)
		}
		if err := outbox.Failed(t.Context(), id); err != nil {
			t.Fatalf("Failed: %v", err)
		}
		if body := outboxBody(t, db, id); body != "" {
			t.Errorf("the abandoned row still holds its body: %q", body)
		}
	}

	// A header with a line break is refused before anything is written.
	{
		bad := domain.Mail{To: "ada@example.com\r\nBcc: attacker@example.com", Subject: "Reset"}
		err := resets.Add(t.Context(), &domain.Reset{
			Hash: "hash-2", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(time.Hour),
		}, bad)
		if !errors.Is(err, domain.ErrBadHeader) {
			t.Fatalf("Add with a line break in To = %v, want ErrBadHeader", err)
		}
		if _, err := resets.Redeem(t.Context(), "hash-2", "x", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("the reset was stored anyway: %v", err)
		}
	}
}

func TestSessions(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessions := store.NewSessions(db, 12*time.Hour)

	// What is committed is found.
	{
		if err := sessions.CommitCtx(t.Context(), "token-1", []byte("data"), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		got, found, err := sessions.FindCtx(t.Context(), "token-1")
		if err != nil || !found || string(got) != "data" {
			t.Errorf("FindCtx = %q, %v, %v; want data, true, nil", got, found, err)
		}
	}

	// Committing the same token again replaces it.
	{
		if err := sessions.CommitCtx(t.Context(), "token-1", []byte("newer"), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		got, _, _ := sessions.FindCtx(t.Context(), "token-1")
		if string(got) != "newer" {
			t.Errorf("FindCtx = %q, want newer", got)
		}
	}

	// This is the one that matters. scs performs no expiry check of its own, so
	// a store that returns expired rows keeps those sessions alive and every
	// request refreshes their idle deadline — IdleTimeout silently off.
	// An expired session is not found, before any janitor runs.
	{
		if err := sessions.CommitCtx(t.Context(), "old", []byte("data"), time.Now().Add(-time.Second)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		_, found, err := sessions.FindCtx(t.Context(), "old")
		if err != nil || found {
			t.Errorf("FindCtx found an expired session (%v)", err)
		}
	}

	// Delete removes it.
	{
		if err := sessions.DeleteCtx(t.Context(), "token-1"); err != nil {
			t.Fatalf("DeleteCtx: %v", err)
		}
		if _, found, _ := sessions.FindCtx(t.Context(), "token-1"); found {
			t.Error("the session survived a delete")
		}
	}

	// The one a logout depends on. A request that loaded the session before the
	// logout landed commits it when its handler returns — scs commits every
	// loaded session under IdleTimeout — and a store that simply deleted the
	// row would take that commit as a new session: the logged-out cookie is
	// signed in again (patterns/go-auth-sessions.md).
	// A deleted token cannot be committed back.
	{
		if err := sessions.CommitCtx(t.Context(), "token-1", []byte("revived"), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		if got, found, _ := sessions.FindCtx(t.Context(), "token-1"); found {
			t.Errorf("a commit after the delete brought the session back: %q", got)
		}
	}

	// Nor can one that was deleted before it was ever stored.
	{
		if err := sessions.DeleteCtx(t.Context(), "never-stored"); err != nil {
			t.Fatalf("DeleteCtx: %v", err)
		}
		if err := sessions.CommitCtx(t.Context(), "never-stored", []byte("data"), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CommitCtx: %v", err)
		}
		if _, found, _ := sessions.FindCtx(t.Context(), "never-stored"); found {
			t.Error("a commit after the delete made a session of a deleted token")
		}
	}

	// The janitor keeps what a delete left.
	{
		if err := sessions.DeleteExpired(t.Context()); err != nil {
			t.Fatalf("DeleteExpired: %v", err)
		}
		var tokens []string
		rows, err := db.Read.QueryContext(t.Context(), "SELECT token FROM sessions ORDER BY token")
		if err != nil {
			t.Fatalf("listing sessions: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var token string
			if err := rows.Scan(&token); err != nil {
				t.Fatalf("scanning a session: %v", err)
			}
			tokens = append(tokens, token)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("reading sessions: %v", err)
		}
		// "old" expired a second ago and stays an hour: the janitor's lag has a
		// test of its own. The two deleted tokens are remembered until their
		// lifetime ends: that memory is what refuses the late commit.
		if want := []string{"never-stored", "old", "token-1"}; !slices.Equal(tokens, want) {
			t.Errorf("rows left = %v, want %v", tokens, want)
		}
	}

	// A deleted token is forgotten an hour after its lifetime has passed.
	{
		brief := store.NewSessions(db, -2*time.Hour) // a lifetime that ended two hours ago
		if err := brief.DeleteCtx(t.Context(), "gone"); err != nil {
			t.Fatalf("DeleteCtx: %v", err)
		}
		if err := brief.DeleteExpired(t.Context()); err != nil {
			t.Fatalf("DeleteExpired: %v", err)
		}
		if _, ok := sessionExpiry(t, db, "gone"); ok {
			t.Error("a row is left for a token whose lifetime ended two hours ago")
		}
	}

	// The mark the rule names: an emptied row is one whose data is NULL.
	// A deleted token's row holds no data.
	{
		var emptied int
		if err := db.Read.QueryRowContext(t.Context(),
			"SELECT count(*) FROM sessions WHERE token = 'token-1' AND data IS NULL").Scan(&emptied); err != nil {
			t.Fatalf("counting: %v", err)
		}
		if emptied != 1 {
			t.Errorf("%d emptied row(s) for the deleted token, want 1", emptied)
		}
	}
}

func TestTokens(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	tokens := store.NewTokens(db)
	ada := addUser(t, db, "Ada")
	bob := addUser(t, db, "Bob")

	token := &domain.Token{ID: "t-1", UserID: ada.ID, Hash: "hash-1", Label: "laptop"}
	if err := tokens.Add(t.Context(), token, ada.PasswordVersion); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// First, while nothing has used the token: Owner must not be what marks it.
	// Owner finds the owner and marks nothing.
	{
		got, err := tokens.Owner(t.Context(), "hash-1")
		if err != nil || got.ID != ada.ID {
			t.Fatalf("Owner = %v, %v; want Ada", got, err)
		}
		if _, err := tokens.Owner(t.Context(), "nothing"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Owner of an unknown hash: err = %v, want ErrNotFound", err)
		}
		listed, err := tokens.ByUser(t.Context(), ada.ID)
		if err != nil || len(listed) != 1 {
			t.Fatalf("ByUser = %v, %v", listed, err)
		}
		if listed[0].LastUsedAt != "" {
			t.Errorf("LastUsedAt = %q after Owner, want it untouched", listed[0].LastUsedAt)
		}
	}

	// The hash finds its owner.
	{
		got, err := tokens.UserByHash(t.Context(), "hash-1")
		if err != nil || got.ID != ada.ID {
			t.Errorf("UserByHash = %v, %v; want Ada", got, err)
		}
	}

	// An unknown hash finds nobody.
	{
		_, err := tokens.UserByHash(t.Context(), "nothing")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	}

	// Using it records when.
	{
		got, err := tokens.ByUser(t.Context(), ada.ID)
		if err != nil || len(got) != 1 {
			t.Fatalf("ByUser = %v, %v", got, err)
		}
		if got[0].LastUsedAt == "" {
			t.Error("LastUsedAt is empty after the token was used")
		}
	}

	// Somebody else's token cannot be revoked.
	{
		err := tokens.Delete(t.Context(), bob.ID, "t-1")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if _, err := tokens.UserByHash(t.Context(), "hash-1"); err != nil {
			t.Errorf("the token stopped working after somebody else tried to revoke it: %v", err)
		}
	}

	// Its owner can.
	{
		if err := tokens.Delete(t.Context(), ada.ID, "t-1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := tokens.UserByHash(t.Context(), "hash-1"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	}
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

// TestTheDSNIsGuarded proves go-sqlite.md rule 10. Unescaped, a "?" in the
// path starts a query string: an innocent one swallows busy_timeout, a crafted
// one carries a _pragma. "#" ends the name and "%" decodes. Escaped, each is
// only a file name, opened with every pragma, and the switch is on.
func TestTheDSNIsGuarded(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "TestX#00 50%")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"a?b.db", "c#d.db", "e%20f.db",
		"g.db?_pragma=foreign_keys(0)",
		"h.db?_pragma=busy_timeout(0)%3BCREATE%20TABLE%20x(a)",
	} {
		db, err := store.Open(t.Context(), filepath.Join(dir, name))
		if err != nil {
			t.Errorf("opening %q: %v", name, err)
			continue
		}
		var busy, fk int
		if err := db.Write.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
			t.Errorf("%q: busy_timeout %d, %v; want 5000", name, busy, err)
		}
		if err := db.Write.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
			t.Errorf("%q: foreign_keys %d, %v; want 1", name, fk, err)
		}
		db.Close()
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%q is not the file it opened: %v", name, err)
		}
	}
	if !sqlite.StrictPragmasEnabled() {
		t.Error("store.Open did not turn on sqlite.StrictPragmas")
	}

	// A leading "//" names a host to SQLite; cleaned, it is the same file.
	db, err := store.Open(t.Context(), "/"+filepath.Join(dir, "a?b.db"))
	if err != nil {
		t.Errorf(`opening "//"+path: %v`, err)
	} else {
		db.Close()
	}
	// An empty path is a private database that vanishes; it must fail instead.
	if db, err := store.Open(t.Context(), ""); err == nil {
		db.Close()
		t.Error("opened an empty path; want an error")
	}
}

// TestLockWaitEndsAtTheDeadline proves go-sqlite.md rule 7: a write waiting for
// a lock another connection holds gives up when its context does, not when
// busy_timeout's five seconds run out.
func TestLockWaitEndsAtTheDeadline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "locked.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer db.Close()

	// A second pool over the file stands in for another process holding the lock.
	other, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("opening the second pool: %v", err)
	}
	defer other.Close()
	holder, err := other.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	defer holder.Rollback()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = db.Write.ExecContext(ctx, "PRAGMA user_version = 99")
	waited := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("write under a held lock: got %v, want context.DeadlineExceeded", err)
	}
	if waited > 2*time.Second {
		t.Errorf("write waited %v; the deadline was 200ms", waited)
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
// outboxBody reads one row's body past the store, which has no reason to offer
// that read: nothing in the app wants the body of a message it is done with.
func outboxBody(t *testing.T, db *store.DB, id string) string {
	t.Helper()
	var body string
	if err := db.Read.QueryRowContext(t.Context(),
		`SELECT body FROM outbox WHERE id = ?`, id).Scan(&body); err != nil {
		t.Fatalf("reading the body of outbox row %s: %v", id, err)
	}
	return body
}

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
	}, ada.PasswordVersion); err != nil {
		t.Fatalf("adding a token: %v", err)
	}
	if err := store.NewResets(db).Add(t.Context(),
		&domain.Reset{Hash: "ada-reset", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(time.Hour)},
		domain.Mail{To: "ada@example.com", Subject: "Reset", Text: "https://example.com/reset?t=x"},
	); err != nil {
		t.Fatalf("adding a reset: %v", err)
	}

	// Mail queued to Ada's address with no user_id: the row a message gets when
	// it goes to an address before that address is anybody's account. No
	// cascade follows an address, so the delete has to reach it by hand — and
	// must leave the same kind of row alone when the address is somebody else's.
	if err := store.NewUsers(db).SetEmail(t.Context(), ada.ID, "ada@example.com"); err != nil {
		t.Fatalf("storing Ada's address: %v", err)
	}
	for id, to := range map[string]string{"to-ada": "ada@example.com", "to-eve": "eve@example.com"} {
		if _, err := db.Write.ExecContext(t.Context(),
			`INSERT INTO outbox (id, recipient, subject, body, created_at) VALUES (?, ?, 'Invitation', 'link', ?)`,
			id, to, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("queueing mail with no user: %v", err)
		}
	}

	// Bob gets one of everything too, so the sweep below proves the cascade is
	// aimed rather than indiscriminate.
	bobMsg := &domain.Message{RoomID: general.ID, AuthorID: bob.ID, Body: "still here"}
	if err := messages.Add(t.Context(), bobMsg, nil); err != nil {
		t.Fatalf("adding bob's message: %v", err)
	}

	if err := store.NewUsers(db).Delete(t.Context(), ada.ID, ada.PasswordVersion); err != nil {
		t.Fatalf("deleting Ada: %v", err)
	}

	for _, table := range tableNames(t, db) {
		for _, col := range textColumns(t, db, table) {
			var n int
			// Identifiers cannot be bound. These come from sqlite_master, never
			// from anything a request touched.
			q := fmt.Sprintf(`SELECT count(*) FROM %q WHERE %q = ?`, table, col)
			if err := db.Read.QueryRowContext(t.Context(), q, ada.ID).Scan(&n); err != nil {
				t.Fatalf("counting %s.%s: %v", table, col, err)
			}
			if n != 0 {
				t.Errorf("%s.%s still holds the deleted user in %d row(s)", table, col, n)
			}
		}
	}

	// The sweep above finds rows that name Ada by id. It cannot find a row that
	// holds her data under another name, and the outbox is exactly that: it
	// stores an address, and an address is not an id. Removing the user_id
	// leaves the sweep green and the address on disk, and so does dropping the
	// delete by address — so both get an assertion of their own.
	var queued int
	if err := db.Read.QueryRowContext(t.Context(),
		`SELECT count(*) FROM outbox WHERE recipient = ?`, "ada@example.com").Scan(&queued); err != nil {
		t.Fatalf("counting queued mail: %v", err)
	}
	if queued != 0 {
		t.Errorf("%d queued message(s) still address the deleted account", queued)
	}
	if err := db.Read.QueryRowContext(t.Context(),
		`SELECT count(*) FROM outbox WHERE recipient = ?`, "eve@example.com").Scan(&queued); err != nil {
		t.Fatalf("counting a stranger's queued mail: %v", err)
	}
	if queued != 1 {
		t.Errorf("%d message(s) left for an address that is not the deleted account's, want 1", queued)
	}

	// The one place neither check can look: a session names its user inside an
	// opaque payload, not in a column. What ends those is authenticate
	// resolving the credential to a row — TestDeletingAnAccountSignsTheBrowserOut.
	var left int
	if err := db.Read.QueryRowContext(t.Context(), `SELECT count(*) FROM messages WHERE author_id = ?`, bob.ID).
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

// sessionExpiry reads one session row's expiry, or reports that there is none.
func sessionExpiry(t *testing.T, db *store.DB, token string) (int64, bool) {
	t.Helper()
	var expiry int64
	err := db.Read.QueryRowContext(t.Context(), "SELECT expiry FROM sessions WHERE token = ?", token).Scan(&expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("reading the expiry of %s: %v", token, err)
	}
	return expiry, true
}

// A logout that arrives after the idle window finds no session, so it empties
// nothing: scs has no token to delete. A request that loaded the session before
// the window closed then commits it, and a store that took that commit would
// sign the cookie in again (patterns/go-auth-sessions.md).
func TestSessionsACommitDoesNotReviveAnExpiredRow(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessions := store.NewSessions(db, 12*time.Hour)

	past := time.Now().Add(-time.Minute)
	if err := sessions.CommitCtx(t.Context(), "idle", []byte("data"), past); err != nil {
		t.Fatalf("CommitCtx: %v", err)
	}
	if err := sessions.CommitCtx(t.Context(), "idle", []byte("late"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("the late CommitCtx: %v", err)
	}
	if got, found, err := sessions.FindCtx(t.Context(), "idle"); err != nil || found {
		t.Errorf("a late commit brought an expired session back: %q, %v", got, err)
	}
	if expiry, ok := sessionExpiry(t, db, "idle"); !ok || expiry != past.Unix() {
		t.Errorf("expiry = %d (row there: %v), want the %d it expired at", expiry, ok, past.Unix())
	}
}

// The janitor only reclaims disk, and it stays an hour behind: a row deleted
// the moment it expires turns the late commit above into an insert.
func TestSessionsTheJanitorLeavesARowAnHourPastItsExpiry(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessions := store.NewSessions(db, 12*time.Hour)

	now := time.Now()
	for token, expiry := range map[string]time.Time{
		"live":           now.Add(time.Hour),
		"just-expired":   now.Add(-time.Second),
		"an-hour-almost": now.Add(-59 * time.Minute),
		"an-hour-past":   now.Add(-61 * time.Minute),
	} {
		if err := sessions.CommitCtx(t.Context(), token, []byte("data"), expiry); err != nil {
			t.Fatalf("CommitCtx %s: %v", token, err)
		}
	}
	if err := sessions.DeleteExpired(t.Context()); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	for token, want := range map[string]bool{
		"live": true, "just-expired": true, "an-hour-almost": true, "an-hour-past": false,
	} {
		if _, ok := sessionExpiry(t, db, token); ok != want {
			t.Errorf("row %s there = %v, want %v", token, ok, want)
		}
	}
	// And the row the janitor kept still refuses the commit it was kept for.
	if err := sessions.CommitCtx(t.Context(), "just-expired", []byte("late"), now.Add(time.Hour)); err != nil {
		t.Fatalf("the late CommitCtx: %v", err)
	}
	if _, found, _ := sessions.FindCtx(t.Context(), "just-expired"); found {
		t.Error("a late commit brought an expired session back after the janitor ran")
	}
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// openAt opens a database file the way Open does and applies the first upTo
// migrations, so a test can stand on the schema an older binary left behind.
// Only the write pool: one connection is all a migration ever uses.
func openAt(t *testing.T, path string, upTo int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+pragmas+"&_txlock=immediate")
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for n := 1; n <= upTo; n++ {
		applyMigration(t, db, n)
	}
	return db
}

// migrationNames is every embedded migration, in the order migrate applies them.
func migrationNames(t *testing.T) []string {
	t.Helper()
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("listing the migrations: %v, %v", names, err)
	}
	return names
}

// applyMigration runs migration n in a transaction, as migrate does.
func applyMigration(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	name := migrationNames(t)[n-1]
	script, err := migrationsFS.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning %s: %v", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), string(script)); err != nil {
		t.Fatalf("applying %s: %v", name, err)
	}
	if _, err := tx.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
		t.Fatalf("stamping %s: %v", name, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing %s: %v", name, err)
	}
}

// referencedTables is every table a foreign key names, as the schema stands
// now — its own key included: a table that references only itself is on the
// list (patterns/go-data-deletion.md rule 6).
func referencedTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(),
		`SELECT DISTINCT lower(f."table")
		   FROM sqlite_schema m, pragma_foreign_key_list(m.name) f
		  WHERE m.type = 'table'
		  ORDER BY 1`)
	if err != nil {
		t.Fatalf("listing referenced tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning a referenced table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading referenced tables: %v", err)
	}
	return tables
}

// takenAway is every table a migration script drops, renames away, or writes
// with REPLACE, lower case. All three take a table's references with them:
// DROP TABLE runs an implicit DELETE that fires every foreign key action below
// it, INSERT OR REPLACE and REPLACE INTO do the same to each row they replace,
// and a table renamed away leaves its children pointing at the new name
// (patterns/go-data-deletion.md rule 6).
func takenAway(script string) []string {
	var words []string
	for line := range strings.Lines(script) {
		line, _, _ = strings.Cut(line, "--")
		words = append(words, strings.Fields(strings.ToLower(line))...)
	}
	name := func(i int) string {
		if i >= len(words) {
			return ""
		}
		return strings.Trim(words[i], "\"`[];")
	}
	var tables []string
	for i := range words {
		// "insert or replace into t" and "replace into t" end the same way.
		if words[i] == "replace" && name(i+1) == "into" {
			table, _, _ := strings.Cut(name(i+2), "(") // "t(a, b)" is written without a space too
			tables = append(tables, table)
			continue
		}
		if i+1 >= len(words) || words[i+1] != "table" {
			continue
		}
		switch words[i] {
		case "drop":
			at := i + 2
			if name(at) == "if" && name(at+1) == "exists" {
				at += 2
			}
			tables = append(tables, name(at))
		case "alter":
			if name(i+3) == "rename" && name(i+4) == "to" {
				tables = append(tables, name(i+2))
			}
		}
	}
	return tables
}

func TestTakenAwayReadsAMigration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		script string
		want   []string
	}{
		{"DROP TABLE users;", []string{"users"}},
		{"drop table if exists \"Users\" ;", []string{"users"}},
		{"ALTER TABLE users RENAME TO users_old;", []string{"users"}},
		{"ALTER TABLE users RENAME COLUMN name TO handle;", nil},
		{"ALTER TABLE users ADD COLUMN x TEXT;\nALTER TABLE users DROP COLUMN y;", nil},
		{"-- DROP TABLE users;\nCREATE TABLE t (a TEXT); -- never drop table users", nil},
		{"CREATE TABLE a (x);\nDROP TABLE sessions;\nDROP TABLE users;", []string{"sessions", "users"}},
		{"INSERT OR REPLACE INTO users (id, name) VALUES ('a', 'Ada');", []string{"users"}},
		{"replace into Users(id, name) values ('a', 'Ada');", []string{"users"}},
		{"INSERT INTO users (id) VALUES ('a') ON CONFLICT (id) DO UPDATE SET name = replace(name, 'a', 'b');", nil},
	} {
		if got := takenAway(tt.script); !slices.Equal(got, tt.want) {
			t.Errorf("takenAway(%q) = %v, want %v", tt.script, got, tt.want)
		}
	}
}

// Rule 6 of patterns/go-data-deletion.md, against every migration this
// repository has: none drops, renames away, or writes with REPLACE a table that
// a foreign key names at the moment it runs, its own key included. Such a
// migration boots green and takes the children with it, and their children, so
// no boot and no integrity check would ever say.
func TestNoMigrationDropsATableAForeignKeyNames(t *testing.T) {
	t.Parallel()
	db := openAt(t, filepath.Join(t.TempDir(), "walk.db"), 0)
	for i, name := range migrationNames(t) {
		referenced := referencedTables(t, db)
		script, err := migrationsFS.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, table := range takenAway(string(script)) {
			if slices.Contains(referenced, table) {
				t.Errorf("%s drops, renames away or REPLACEs into %s, which a foreign key names: change it in place", name, table)
			}
		}
		applyMigration(t, db, i+1)
	}
	// The walk above passes on an empty list, so say what it stood on.
	if got := referencedTables(t, db); !slices.Contains(got, "users") {
		t.Errorf("referenced tables = %v: the walk never saw users as a parent", got)
	}
}

// The rule's "its own included": a table whose only foreign key points at
// itself — a thread of comments — is a table a foreign key names, and the walk
// above has to see it as one. Rebuilt the ordinary way it loses its replies
// under CASCADE and every parent under SET NULL, with a green boot. This app
// has no such table, so the helper is held to one made here.
func TestATableThatReferencesOnlyItselfIsOneAForeignKeyNames(t *testing.T) {
	t.Parallel()
	db := openAt(t, filepath.Join(t.TempDir(), "self.db"), 0)
	if _, err := db.ExecContext(t.Context(),
		`CREATE TABLE comments (
		     id     INTEGER PRIMARY KEY,
		     parent INTEGER REFERENCES comments(id) ON DELETE CASCADE
		 )`); err != nil {
		t.Fatalf("creating a table that references itself: %v", err)
	}
	if got := referencedTables(t, db); !slices.Equal(got, []string{"comments"}) {
		t.Errorf("referenced tables = %v, want comments: no other table names it, and its own key does", got)
	}
}

// And the statements the store itself runs, which no migration walk reads: none
// says REPLACE. The walk is over this package's own source, because a statement
// is a string in it. No table is exempt here, though the rule is about a table
// a foreign key names: ON CONFLICT … DO UPDATE is the same write on every
// table, and the next foreign key is one migration away.
func TestNoStatementInTheStoreSaysReplace(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("listing the store's source: %v, %v", files, err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if tables := takenAway(string(src)); len(tables) != 0 {
			t.Errorf("%s drops, renames away or REPLACEs into %v: write ON CONFLICT … DO UPDATE", file, tables)
		}
	}
}

// Migration 0005 against a database that has people in it: the counter arrives
// at zero, the time is gone, and every row that hangs off a user is still
// there. It drops sessions, which nothing references, and those rows go.
func TestThePasswordCounterMigrationKeepsEveryRow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	old := openAt(t, path, 4)
	for _, stmt := range []string{
		`INSERT INTO users (id, name, name_key, password_hash, created_at, email, password_changed_at)
		 VALUES ('u1', 'Ada', 'ada', 'hash', '2026-01-01T00:00:00Z', 'ada@example.com', '2026-02-02T00:00:00Z')`,
		`INSERT INTO rooms (id, slug, name, created_at) VALUES ('r1', 'general', 'General', '2026-01-01T00:00:00Z')`,
		`INSERT INTO messages (room_id, author_id, body, created_at) VALUES ('r1', 'u1', 'hello', '2026-01-01T00:00:00Z')`,
		`INSERT INTO attachments (id, message_seq, uploader_id, name, kind, size, bytes, created_at)
		 VALUES ('a1', 1, 'u1', 'a.txt', 'text/plain; charset=utf-8', 1, x'61', '2026-01-01T00:00:00Z')`,
		`INSERT INTO tokens (id, user_id, hash, label, created_at) VALUES ('t1', 'u1', 'token-hash', 'laptop', '2026-01-01T00:00:00Z')`,
		`INSERT INTO resets (hash, user_id, expires_at, created_at) VALUES ('reset-hash', 'u1', '2026-01-01T01:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO outbox (id, user_id, recipient, subject, body, created_at)
		 VALUES ('o1', 'u1', 'ada@example.com', 'Reset', 'body', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions (token, data, expiry) VALUES ('s1', x'01', 4102444800)`,
	} {
		if _, err := old.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("seeding the old database: %v\n%s", err, stmt)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatalf("closing the old database: %v", err)
	}

	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening the old database with this binary: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for table, want := range map[string]int{
		"users": 2, // Ada, and the assistant migration 0002 seeds
		"rooms": 1, "messages": 1, "attachments": 1, "tokens": 1, "resets": 1, "outbox": 1,
		"sessions": 0,
	} {
		if got := countRows(t.Context(), t, db.Read, table); got != want {
			t.Errorf("%s holds %d row(s) after the migration, want %d", table, got, want)
		}
	}
	var version int64
	var hash, email string
	if err := db.Read.QueryRowContext(t.Context(),
		`SELECT password_version, password_hash, email FROM users WHERE id = 'u1'`).Scan(&version, &hash, &email); err != nil {
		t.Fatalf("reading Ada after the migration: %v", err)
	}
	if version != 0 || hash != "hash" || email != "ada@example.com" {
		t.Errorf("Ada = version %d, hash %q, email %q; want 0, hash, ada@example.com", version, hash, email)
	}
	var stale int
	if err := db.Read.QueryRowContext(t.Context(),
		`SELECT count(*) FROM pragma_table_info('users') WHERE name = 'password_changed_at'`).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("password_changed_at is still a column (%d, %v)", stale, err)
	}
	var broken int
	if err := db.Read.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
		t.Errorf("foreign_key_check reports %d row(s) (%v), want none", broken, err)
	}
}

// countRows counts a table's rows. The name is one of this test's own
// literals, never input.
func countRows(ctx context.Context, t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

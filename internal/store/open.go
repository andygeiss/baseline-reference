// Package store persists tasks in SQLite, configured per the engineering
// baseline: WAL mode, pooled readers, a single writer.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"runtime"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const pragmas = "?_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=foreign_keys(1)"

// DB bundles the read pool and the single-connection write pool over one file.
type DB struct {
	Read  *sql.DB
	Write *sql.DB
}

func Open(ctx context.Context, path string) (*DB, error) {
	read, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		return nil, fmt.Errorf("opening read pool: %w", err)
	}
	read.SetMaxOpenConns(max(4, runtime.NumCPU()))

	write, err := sql.Open("sqlite", "file:"+path+pragmas+"&_txlock=immediate")
	if err != nil {
		read.Close()
		return nil, fmt.Errorf("opening write pool: %w", err)
	}
	write.SetMaxOpenConns(1)

	db := &DB{Read: read, Write: write}
	if err := db.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating: %w", err)
	}
	return db, nil
}

func (db *DB) Close() error {
	rerr := db.Read.Close()
	werr := db.Write.Close()
	if rerr != nil {
		return rerr
	}
	return werr
}

// migrate applies embedded migrations newer than PRAGMA user_version, in order.
func (db *DB) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}

	tx, err := db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}

	for i, name := range names { // fs.Glob returns sorted names
		n := i + 1
		if n <= version {
			continue
		}
		script, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("applying %s: %w", name, err)
		}
		version = n
	}

	// PRAGMA does not accept placeholders; version is derived from the file count.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

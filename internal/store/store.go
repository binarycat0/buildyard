// Package store owns the SQLite database: opening it, migrations and queries.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a unique constraint is violated.
	ErrConflict = errors.New("already exists")
)

// Store wraps the database handle.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection keeps things simple and avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, db, mustSub(migrations, "migrations"))
	if err != nil {
		return err
	}
	if _, err = p.Up(ctx); err != nil {
		return err
	}
	return ensureColumns(ctx, db)
}

// columns are added by migrations, listed again here so a database whose version
// table already counts that migration number gets them anyway. That happens when a
// development build applied a different migration under the same number (a branch
// that was never merged): goose then skips the real one.
var columns = []struct{ table, column, ddl string }{
	{"job", "trace_id", "ALTER TABLE job ADD COLUMN trace_id TEXT"},
}

func ensureColumns(ctx context.Context, db *sql.DB) error {
	for _, c := range columns {
		var n int
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n)
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.ExecContext(ctx, c.ddl); err != nil {
				return fmt.Errorf("add %s.%s: %w", c.table, c.column, err)
			}
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

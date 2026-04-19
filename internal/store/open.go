package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/errors"
)

// Open establishes a live SQLite connection pool against path and
// returns the Store interface backed by an unexported *sqliteStore.
// The DSN carries `?_txlock=immediate` so every BeginTx(ctx, nil)
// issues BEGIN IMMEDIATE; per-connection pragmas (busy_timeout,
// foreign_keys) are applied by the init() hook in conn.go. Open does
// NOT run migrations — the dispatcher (Task 4.2 step 5) calls
// store.Migrate separately so the migrate span is a sibling of the
// command span per OTEL.md §8.8.
//
// journal_mode=WAL is a database-header pragma and is persistent once
// set; Open issues it on the primary connection and asserts the
// effective mode is "wal" before returning.
func Open(path string) (Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: store.Open: empty path", errors.ErrGeneral)
	}
	dsn := "file:" + path + "?_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, classifyDriverErr(err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, classifyDriverErr(err)
	}
	// journal_mode=WAL is persistent once set; apply it once on a
	// pool-allocated connection. The driver reports the effective mode
	// in the response row, so check the result to catch a silent
	// downgrade (e.g., a read-only filesystem leaving the DB in
	// 'delete' mode).
	var mode string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		_ = db.Close()
		return nil, classifyDriverErr(err)
	}
	if mode != "wal" {
		_ = db.Close()
		return nil, fmt.Errorf("%w: PRAGMA journal_mode=WAL returned %q", errors.ErrGeneral, mode)
	}
	return &sqliteStore{db: db}, nil
}

type sqliteStore struct {
	db *sql.DB
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

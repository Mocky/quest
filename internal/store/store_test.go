//go:build integration

package store_test

import (
	"context"
	"database/sql"
	stderrors "errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "quest.db")
}

func TestOpenAppliesPragmas(t *testing.T) {
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Open a sibling sql.DB to inspect the journal_mode on the same
	// file — the store interface deliberately does not expose the raw
	// *sql.DB.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.QueryRow("SELECT journal_mode FROM pragma_journal_mode").Scan(&mode); err != nil {
		t.Fatalf("pragma_journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestOpenDoesNotCreateSchema(t *testing.T) {
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	v, err := s.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion: %v", err)
	}
	if v != 0 {
		t.Fatalf("CurrentSchemaVersion = %d, want 0 on fresh DB", v)
	}

	// No tables should exist yet — spot-check a few the migrations
	// will create.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	for _, tbl := range []string{"tasks", "history", "meta"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err != sql.ErrNoRows {
			t.Fatalf("table %s: expected sql.ErrNoRows, got %v (name=%q)", tbl, err, name)
		}
	}
}

func TestBeginImmediateIsImmediate(t *testing.T) {
	// Two sibling *store.Store instances against the same file.
	// First holds the write lock; second attempts BeginImmediate
	// before the first commits and must block until it does.
	path := tempDB(t)
	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	defer s1.Close()
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	defer s2.Close()

	ctx := context.Background()

	holding := make(chan struct{})
	release := make(chan struct{})
	go func() {
		tx, err := s1.BeginImmediate(ctx, store.TxCreate)
		if err != nil {
			t.Errorf("s1 BeginImmediate: %v", err)
			close(holding)
			return
		}
		close(holding)
		<-release
		_ = tx.Commit()
	}()

	<-holding
	start := time.Now()
	done := make(chan struct{})
	go func() {
		tx, err := s2.BeginImmediate(ctx, store.TxCreate)
		if err == nil {
			_ = tx.Commit()
		}
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("s2 BeginImmediate returned before s1 released (after %s)", time.Since(start))
	case <-time.After(200 * time.Millisecond):
		// Expected: s2 is still waiting on the write lock.
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("s2 BeginImmediate never returned after s1 released")
	}
}

func TestConcurrentReaderDoesNotBlockWriter(t *testing.T) {
	// Seed a tiny one-row table so the reader has something to read.
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	seed, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open seed: %v", err)
	}
	defer seed.Close()
	if _, err := seed.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatalf("create t: %v", err)
	}
	for i := 0; i < 200; i++ {
		if _, err := seed.Exec(`INSERT INTO t(x) VALUES(?)`, i); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	reader, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open reader: %v", err)
	}
	defer reader.Close()

	ctx := context.Background()

	// Start a slow reader that holds a read lock.
	readerBlocked := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		rows, err := reader.QueryContext(ctx, `SELECT x FROM t ORDER BY x`)
		if err != nil {
			t.Errorf("reader query: %v", err)
			close(readerBlocked)
			close(readerDone)
			return
		}
		close(readerBlocked)
		count := 0
		for rows.Next() {
			var x int
			_ = rows.Scan(&x)
			count++
			time.Sleep(2 * time.Millisecond)
		}
		rows.Close()
		close(readerDone)
	}()

	<-readerBlocked

	// Writer should acquire immediately despite the reader holding a
	// read lock (WAL allows concurrent readers + one writer).
	start := time.Now()
	tx, err := s.BeginImmediate(ctx, store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate while reader active: %v", err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("BeginImmediate took %s — reader blocked writer", el)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	<-readerDone
}

// TestReadOnlyTxBypassesTxLock pins the documented driver caveat:
// since modernc.org/sqlite v1.20.3, _txlock=immediate is silently
// ignored on transactions opened with sql.TxOptions{ReadOnly: true} —
// they issue plain deferred BEGIN instead. Quest's Store interface
// deliberately does NOT expose a read-only transaction opener; this
// test proves the caveat is real by opening a read-only tx via direct
// SQL (bypassing the production API) and confirming it does not block
// on a held write lock. If this test ever fails, either the driver's
// behavior changed or someone added a read-only path to Store — both
// require revisiting the exit-7 contract.
func TestReadOnlyTxBypassesTxLock(t *testing.T) {
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	db, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx := context.Background()
	writer, err := s.BeginImmediate(ctx, store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		ro, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			done <- err
			return
		}
		// Read-only tx opened while writer is active: must not block.
		rows, err := ro.QueryContext(ctx, `SELECT x FROM t`)
		if err != nil {
			done <- err
			return
		}
		rows.Close()
		done <- ro.Rollback()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read-only tx: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("read-only BeginTx blocked on write lock — driver caveat has changed, revisit exit-7 contract")
	}

	if err := writer.Commit(); err != nil {
		t.Fatalf("writer commit: %v", err)
	}
}

func TestExecContextAccumulatesRowsAffected(t *testing.T) {
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tx, err := s.BeginImmediate(ctx, store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO t(x) VALUES (1),(2),(3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE t SET x=x+10`); err != nil {
		t.Fatalf("update: %v", err)
	}
	// 3 inserts + 3 updates = 6 rows_affected accumulated.
	if got := tx.RowsAffected(); got != 6 {
		t.Fatalf("RowsAffected = %d, want 6", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if tx.Outcome() != store.TxCommitted {
		t.Fatalf("Outcome = %q, want %q", tx.Outcome(), store.TxCommitted)
	}
}

func TestRollbackDefersSafely(t *testing.T) {
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	tx, err := s.BeginImmediate(ctx, store.TxUpdate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Deferred rollback after commit must be a no-op, not surface an
	// error back to the handler.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback after commit: %v", err)
	}
}

func TestCurrentSchemaVersionReadsMeta(t *testing.T) {
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if v, err := s.CurrentSchemaVersion(ctx); err != nil || v != 0 {
		t.Fatalf("fresh CurrentSchemaVersion = (%d, %v), want (0, nil)", v, err)
	}

	// Seed a meta table directly to simulate a migrated DB.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', '7')`); err != nil {
		t.Fatalf("insert meta: %v", err)
	}

	if v, err := s.CurrentSchemaVersion(ctx); err != nil || v != 7 {
		t.Fatalf("CurrentSchemaVersion = (%d, %v), want (7, nil)", v, err)
	}
}

func TestConcurrentWritersSerialize(t *testing.T) {
	// Two writers racing on the same DB: both must commit, the
	// second blocking on busy_timeout and then proceeding. The goal
	// is that neither returns ErrTransient within the 5s budget.
	path := tempDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
			if err != nil {
				results <- err
				return
			}
			if _, err := tx.ExecContext(context.Background(), `INSERT INTO t(x) VALUES(?)`, n); err != nil {
				_ = tx.Rollback()
				results <- err
				return
			}
			// Hold briefly to create contention.
			time.Sleep(50 * time.Millisecond)
			results <- tx.Commit()
		}(i)
	}
	wg.Wait()
	close(results)

	for err := range results {
		if err != nil {
			t.Fatalf("concurrent writer: %v", err)
		}
	}

	// Both inserts landed.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestOpenEmptyPathReturnsErrGeneral(t *testing.T) {
	if _, err := store.Open(""); err == nil {
		t.Fatalf("Open(\"\"): got nil, want ErrGeneral")
	} else if !isGeneral(err) {
		t.Fatalf("Open(\"\"): err = %v, want wraps ErrGeneral", err)
	}
}

func isGeneral(err error) bool {
	return stderrors.Is(err, errors.ErrGeneral)
}

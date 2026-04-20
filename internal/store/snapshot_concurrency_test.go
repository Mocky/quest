//go:build integration

package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/store"
)

// TestSnapshotDuringWrite exercises the "safe to run while agents
// operate" guarantee from spec §`quest backup`. A background goroutine
// streams BeginImmediate writes while the main goroutine takes
// repeated snapshots; both must succeed, and every snapshot must be a
// readable SQLite database.
//
// If this reveals that Step(-1) contends with writers more than
// expected, downgrade the primitive to Step(100) in a loop with a
// runtime.Gosched() yield. The spec's hot-backup guarantee is the
// most likely source of surprise at scale.
func TestSnapshotDuringWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping snapshot concurrency test in -short")
	}
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	stop := make(chan struct{})
	var writerErr atomic.Value
	var writeCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := context.Background()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			tx, err := s.BeginImmediate(ctx, store.TxCreate)
			if err != nil {
				writerErr.Store(err)
				return
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO tasks(id, title, created_at) VALUES (?, ?, ?)`,
				fmt.Sprintf("proj-w%d", i), "writer", "2026-04-20T00:00:00Z"); err != nil {
				_ = tx.Rollback()
				writerErr.Store(err)
				return
			}
			if err := tx.Commit(); err != nil {
				writerErr.Store(err)
				return
			}
			writeCount.Add(1)
			i++
		}
	}()

	snapDir := t.TempDir()
	const snapshots = 5
	for i := 0; i < snapshots; i++ {
		out := filepath.Join(snapDir, fmt.Sprintf("snap-%d.db", i))
		if _, err := s.Snapshot(context.Background(), out); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Snapshot[%d]: %v", i, err)
		}
		copy, err := store.Open(out)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("reopen snap-%d: %v", i, err)
		}
		if _, err := copy.ListTasks(context.Background(), store.Filter{}); err != nil {
			_ = copy.Close()
			close(stop)
			wg.Wait()
			t.Fatalf("ListTasks on snap-%d: %v", i, err)
		}
		_ = copy.Close()
		// Brief sleep so the writer makes forward progress between
		// snapshots — a tight snapshot loop can otherwise starve the
		// writer on small DBs.
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	if v := writerErr.Load(); v != nil {
		t.Fatalf("writer saw error: %v", v.(error))
	}
	if writeCount.Load() == 0 {
		t.Errorf("writer made zero commits during snapshots; test did not exercise concurrency")
	}
}

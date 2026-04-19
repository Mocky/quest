//go:build integration

package ids_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/ids"
	"github.com/mocky/quest/internal/store"
)

// TestNewTopLevelConcurrent proves the counter allocation is
// collision-free under serialized write contention. 50 goroutines each
// open their own BeginImmediate transaction, allocate, and commit;
// SQLite serializes the write lock (5s busy_timeout) so all 50 IDs come
// out distinct. No goroutine retries — exit-7 is the caller's concern,
// not the generator's.
func TestNewTopLevelConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if _, err := store.Migrate(ctx, s); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	const N = 50
	var wg sync.WaitGroup
	results := make(chan string, N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := s.BeginImmediate(ctx, store.TxCreate)
			if err != nil {
				errs <- err
				return
			}
			id, err := ids.NewTopLevel(ctx, tx, "proj")
			if err != nil {
				_ = tx.Rollback()
				errs <- err
				return
			}
			if err := tx.Commit(); err != nil {
				errs <- err
				return
			}
			results <- id
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("worker: %v", err)
		}
	}
	seen := make(map[string]struct{}, N)
	for id := range results {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != N {
		t.Fatalf("got %d distinct ids, want %d", len(seen), N)
	}
}

// TestNewSubTaskPerParentCounter confirms the sub-task counter restarts
// at 1 for each parent — spec §ID generation rules ("separate per-parent
// base10 counter, starting at `1`").
func TestNewSubTaskPerParentCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if _, err := store.Migrate(ctx, s); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	tx, err := s.BeginImmediate(ctx, store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()

	// The subtask_counter row has a FK to tasks(id); seed the parents
	// so the ON CONFLICT branch can insert. These rows stand in for
	// real tasks that Phase 7 (create) will produce.
	for _, p := range []string{"proj-01", "proj-02", "proj-01.1"} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks(id, title, created_at) VALUES (?, ?, ?)`,
			p, "seed", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	cases := []struct {
		parent string
		want   []string
	}{
		{"proj-01", []string{"proj-01.1", "proj-01.2", "proj-01.3"}},
		{"proj-02", []string{"proj-02.1", "proj-02.2"}},
		{"proj-01.1", []string{"proj-01.1.1", "proj-01.1.2"}},
	}
	for _, tc := range cases {
		for _, want := range tc.want {
			got, err := ids.NewSubTask(ctx, tx, tc.parent)
			if err != nil {
				t.Fatalf("NewSubTask(%q): %v", tc.parent, err)
			}
			if got != want {
				t.Fatalf("NewSubTask(%q) = %q, want %q", tc.parent, got, want)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestNewTopLevelFormatBoundary exercises the 2→3 char width transition
// through a real store: allocating enough top-level ids to cross the
// 1295→1296 boundary and confirming the 1296th id is "proj-100".
// Marked slow but still runs inside a second — the counter update is
// one round-trip.
func TestNewTopLevelFormatBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping allocator boundary test in -short")
	}
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if _, err := store.Migrate(ctx, s); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	tx, err := s.BeginImmediate(ctx, store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()

	var first, last, overflow string
	for i := 1; i <= 1296; i++ {
		id, err := ids.NewTopLevel(ctx, tx, "proj")
		if err != nil {
			t.Fatalf("NewTopLevel(%d): %v", i, err)
		}
		switch i {
		case 1:
			first = id
		case 1295:
			last = id
		case 1296:
			overflow = id
		}
	}
	if first != "proj-01" {
		t.Fatalf("id 1 = %q, want proj-01", first)
	}
	if last != "proj-zz" {
		t.Fatalf("id 1295 = %q, want proj-zz", last)
	}
	if overflow != "proj-100" {
		t.Fatalf("id 1296 = %q, want proj-100", overflow)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

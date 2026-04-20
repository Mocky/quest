//go:build integration

package store_test

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// snapshotSeed opens a fresh store, runs the full migration, and
// inserts two task rows so the copy has meaningful content to verify
// against. Returns the live store, its DB path, and the two task IDs.
func snapshotSeed(t *testing.T) (store.Store, string, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	ctx := context.Background()
	ids := []string{"proj-01", "proj-02"}
	for _, id := range ids {
		tx, err := s.BeginImmediate(ctx, store.TxCreate)
		if err != nil {
			t.Fatalf("BeginImmediate: %v", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks(id, title, created_at) VALUES (?, ?, ?)`,
			id, "Task "+id, "2026-04-20T00:00:00Z"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert %s: %v", id, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", id, err)
		}
	}
	return s, path, ids
}

// TestSnapshotProducesReadableCopy is the core happy-path assertion:
// the copied file is a valid sqlite DB that contains the same tasks as
// the source. Closes the loop end-to-end without touching CLI code.
func TestSnapshotProducesReadableCopy(t *testing.T) {
	s, _, ids := snapshotSeed(t)
	out := filepath.Join(t.TempDir(), "snap.db")

	bytes, err := s.Snapshot(context.Background(), out)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if bytes <= 0 {
		t.Fatalf("Snapshot bytes = %d, want > 0", bytes)
	}

	copy, err := store.Open(out)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	defer copy.Close()

	tasks, err := copy.ListTasks(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != len(ids) {
		t.Fatalf("snapshot tasks = %d, want %d", len(tasks), len(ids))
	}
	got := map[string]bool{}
	for _, tt := range tasks {
		got[tt.ID] = true
	}
	for _, id := range ids {
		if !got[id] {
			t.Errorf("snapshot missing task %s", id)
		}
	}
}

// TestSnapshotBytesMatchesFileSize: the returned byte count is the
// on-disk size of the produced file.
func TestSnapshotBytesMatchesFileSize(t *testing.T) {
	s, _, _ := snapshotSeed(t)
	out := filepath.Join(t.TempDir(), "snap.db")

	bytes, err := s.Snapshot(context.Background(), out)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if bytes != fi.Size() {
		t.Errorf("bytes = %d, file size = %d", bytes, fi.Size())
	}
}

// TestSnapshotDestinationNotWritable points dstPath at a read-only
// directory and asserts the error wraps ErrGeneral (transient errors
// are reserved for SQLITE_BUSY on the source side).
func TestSnapshotDestinationNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o500 semantics differ on Windows")
	}
	s, _, _ := snapshotSeed(t)

	roDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatalf("mkdir readonly: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	_, err := s.Snapshot(context.Background(), filepath.Join(roDir, "snap.db"))
	if err == nil {
		t.Fatalf("Snapshot into read-only dir: got nil err")
	}
	if !stderrors.Is(err, errors.ErrGeneral) {
		t.Errorf("err = %v, want wraps ErrGeneral", err)
	}
}

// TestSnapshotOverwritesExistingFile mirrors the spec idempotency note:
// a snapshot at an existing path replaces the file with a fresh copy.
func TestSnapshotOverwritesExistingFile(t *testing.T) {
	s, _, ids := snapshotSeed(t)
	out := filepath.Join(t.TempDir(), "snap.db")

	// Write junk at the destination first.
	if err := os.WriteFile(out, []byte("not a sqlite file"), 0o644); err != nil {
		t.Fatalf("WriteFile junk: %v", err)
	}

	if _, err := s.Snapshot(context.Background(), out); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	copy, err := store.Open(out)
	if err != nil {
		t.Fatalf("reopen overwritten snapshot: %v", err)
	}
	defer copy.Close()
	tasks, err := copy.ListTasks(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListTasks on overwritten: %v", err)
	}
	if len(tasks) != len(ids) {
		t.Errorf("overwritten snapshot tasks = %d, want %d", len(tasks), len(ids))
	}
}

// TestPreMigrationSnapshotWritesTimestampedFile drives the shared
// helper with a live store. Asserts the produced path lives under
// .quest/backups/, matches the `pre-v{N}-{timestamp}.db` shape, and
// contains the source's data.
func TestPreMigrationSnapshotWritesTimestampedFile(t *testing.T) {
	s, _, ids := snapshotSeed(t)

	// The helper resolves the workspace root relative to its argument.
	// Reuse the store's tempdir by locating its parent.
	// snapshotSeed puts the DB at <tempdir>/quest.db, so workspace root
	// here is the tempdir above that — but the helper expects to be
	// pointed at a workspace root with .quest/ under it. Stand one up.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".quest"), 0o755); err != nil {
		t.Fatalf("mkdir .quest: %v", err)
	}

	path, err := store.PreMigrationSnapshot(context.Background(), root, s, 42)
	if err != nil {
		t.Fatalf("PreMigrationSnapshot: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(root, ".quest", "backups") {
		t.Errorf("path dir = %q, want %q", filepath.Dir(path), filepath.Join(root, ".quest", "backups"))
	}
	base := filepath.Base(path)
	if len(base) < len("pre-v42-YYYYMMDDTHHMMSSZ.db") {
		t.Errorf("unexpected filename shape: %q", base)
	}

	copy, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	defer copy.Close()
	tasks, err := copy.ListTasks(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != len(ids) {
		t.Errorf("tasks = %d, want %d", len(tasks), len(ids))
	}
}

// TestPreMigrationSnapshotMkdirFailure surfaces a wrapped ErrGeneral
// when the .quest/backups/ directory cannot be created (e.g., because
// a non-directory file already sits at that path). Load-bearing because
// the dispatcher maps this error onto the "migration did not run" path.
func TestPreMigrationSnapshotMkdirFailure(t *testing.T) {
	s, _, _ := snapshotSeed(t)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".quest"), 0o755); err != nil {
		t.Fatalf("mkdir .quest: %v", err)
	}
	// Put a regular file where .quest/backups/ should live, so
	// MkdirAll fails.
	if err := os.WriteFile(filepath.Join(root, ".quest", "backups"), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}

	_, err := store.PreMigrationSnapshot(context.Background(), root, s, 2)
	if err == nil {
		t.Fatalf("PreMigrationSnapshot: got nil err")
	}
	if !stderrors.Is(err, errors.ErrGeneral) {
		t.Errorf("err = %v, want wraps ErrGeneral", err)
	}
}

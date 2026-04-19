//go:build integration

package store_test

import (
	"context"
	"database/sql"
	stderrors "errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

func TestMigrateFreshAppliesSchemaV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	applied, err := store.Migrate(context.Background(), s)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	v, err := s.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion: %v", err)
	}
	if v != store.SupportedSchemaVersion {
		t.Fatalf("version = %d, want %d", v, store.SupportedSchemaVersion)
	}

	// Every schema-v1 table must exist.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	wantTables := []string{"meta", "tasks", "history", "dependencies", "tags", "prs", "notes", "task_counter", "subtask_counter"}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[n] = true
	}
	for _, want := range wantTables {
		if !got[want] {
			t.Errorf("missing table %q (got %v)", want, sortedKeys(got))
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestMigrateAlreadyAtHeadIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	applied, err := store.Migrate(context.Background(), s)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if applied != 0 {
		t.Fatalf("second Migrate applied = %d, want 0", applied)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	// Simulate a DB written by a newer binary: meta table exists,
	// schema_version = SupportedSchemaVersion + 1.
	path := filepath.Join(t.TempDir(), "quest.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', ?)`, store.SupportedSchemaVersion+1); err != nil {
		t.Fatalf("insert meta: %v", err)
	}
	_ = db.Close()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	applied, err := store.Migrate(context.Background(), s)
	if err == nil {
		t.Fatalf("Migrate: got nil error, want schema-too-new")
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0", applied)
	}
	if !stderrors.Is(err, errors.ErrGeneral) {
		t.Fatalf("err = %v, want wraps ErrGeneral", err)
	}
	// Spec-pinned wording (quest-spec.md §Storage).
	if !strings.Contains(err.Error(), "is newer than this binary supports") {
		t.Fatalf("err message %q missing spec-pinned wording", err.Error())
	}

	// DB must be untouched: version still at SupportedSchemaVersion+1.
	v, err := s.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion: %v", err)
	}
	if v != store.SupportedSchemaVersion+1 {
		t.Fatalf("version = %d, want %d (DB must be untouched)", v, store.SupportedSchemaVersion+1)
	}
}

func TestMigrationEnforcesForeignKeys(t *testing.T) {
	// Connect hook sets foreign_keys=ON per-connection. Inserting a
	// task with a non-existent parent must fail with a FK violation.
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, parent, created_at) VALUES ('t1', 'child', 'does-not-exist', '2026-04-18T00:00:00Z')`)
	if err == nil {
		t.Fatalf("insert with missing parent: got nil error, want FK violation")
	}
	_ = tx.Rollback()
}

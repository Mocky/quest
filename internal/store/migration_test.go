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
	"testing/fstest"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

func TestMigrateFreshAppliesEverySchema(t *testing.T) {
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
	if applied != store.SupportedSchemaVersion {
		t.Fatalf("applied = %d, want %d", applied, store.SupportedSchemaVersion)
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

// TestMigrateV2RenamesCompleteStatusRows exercises migration 002 on a
// DB that still carries pre-rename `status = 'complete'` rows from a
// v1-era binary. Bootstrap: let Migrate build schema v1 (and higher),
// then roll schema_version back to 1 and seed rows with the old value
// — the next Migrate call replays 002+ against them.
func TestMigrateV2RenamesCompleteStatusRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("initial Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '1' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("reset schema_version: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO tasks(id, title, status, created_at) VALUES
			('proj-c1', 'Done', 'complete', '2026-04-18T00:00:00Z'),
			('proj-c2', 'Open',  'open',     '2026-04-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed tasks: %v", err)
	}
	_ = db.Close()

	applied, err := store.Migrate(context.Background(), s)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if applied != store.SupportedSchemaVersion-1 {
		t.Fatalf("applied = %d, want %d", applied, store.SupportedSchemaVersion-1)
	}

	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var stillComplete int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status = 'complete'`).Scan(&stillComplete); err != nil {
		t.Fatalf("count complete: %v", err)
	}
	if stillComplete != 0 {
		t.Errorf("rows with status='complete' = %d, want 0", stillComplete)
	}

	var renamed string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = 'proj-c1'`).Scan(&renamed); err != nil {
		t.Fatalf("select proj-c1: %v", err)
	}
	if renamed != "completed" {
		t.Errorf("proj-c1 status = %q, want 'completed'", renamed)
	}

	var untouched string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = 'proj-c2'`).Scan(&untouched); err != nil {
		t.Fatalf("select proj-c2: %v", err)
	}
	if untouched != "open" {
		t.Errorf("proj-c2 status = %q, want 'open' (untouched)", untouched)
	}

	var version string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("select schema_version: %v", err)
	}
	if version != "2" {
		t.Errorf("schema_version = %q, want '2'", version)
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

// TestMigrationFailureRollsBack pins the spec's forward-only-never-partial
// promise (quest-spec.md §Storage): when a migration fails mid-execution
// the transaction rolls back, schema_version stays at the prior value,
// no rows from the failing migration persist, and the error names the
// failing migration. Re-running against a working migration set after
// the failure then lands at head.
func TestMigrationFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Poisoned set: a minimal valid 001 that creates meta + bumps
	// schema_version, followed by a 002 that starts with real SQL
	// (so there is state to roll back) and then hits an unresolvable
	// reference. fstest.MapFS lets the test ship its own migration set
	// without touching the embedded FS.
	poisoned := fstest.MapFS{
		"001_initial.sql": &fstest.MapFile{Data: []byte(`
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO meta(key, value) VALUES ('schema_version', '1');
`)},
		"002_poisoned.sql": &fstest.MapFile{Data: []byte(`
CREATE TABLE poisoned_evidence (id INTEGER PRIMARY KEY);
UPDATE meta SET value = '2' WHERE key = 'schema_version';
INSERT INTO nonexistent_table(id) VALUES (1);
`)},
	}

	applied, err := store.MigrateFromFS(context.Background(), s, poisoned)
	if err == nil {
		t.Fatalf("MigrateFromFS on poisoned set: got nil error, want migration failure")
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (failure returns 0 on the error path)", applied)
	}
	if !stderrors.Is(err, errors.ErrGeneral) {
		t.Errorf("err = %v, want wraps ErrGeneral", err)
	}
	// The error text must name which migration failed so operators
	// and agents can locate the broken file.
	if !strings.Contains(err.Error(), "002") || !strings.Contains(err.Error(), "poisoned") {
		t.Errorf("err = %q, want migration identifier (\"002\" and label) in message", err.Error())
	}

	// After rollback the DB must be indistinguishable from pre-migration:
	// no meta table (so CurrentSchemaVersion returns 0), and no rows from
	// the failing migration persist. Probe sqlite_master directly since
	// CurrentSchemaVersion already returns 0 when meta is absent.
	v, err := s.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion after rollback: %v", err)
	}
	if v != 0 {
		t.Errorf("schema_version after rollback = %d, want 0 (transaction must have rolled back 001 too)", v)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	for _, table := range []string{"meta", "poisoned_evidence"} {
		var name string
		row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table)
		if err := row.Scan(&name); err == nil {
			t.Errorf("table %q exists after rollback, want none (forward-only-never-partial violated)", table)
		} else if !stderrors.Is(err, sql.ErrNoRows) {
			t.Fatalf("sqlite_master lookup for %q: %v", table, err)
		}
	}

	// Re-running with the real (embedded) migration set after the
	// failure must succeed and land at head. This pins the spec's
	// implicit recovery contract: a partial failure does not wedge the
	// DB — the next Migrate run against a fixed set reaches head.
	applied, err = store.Migrate(context.Background(), s)
	if err != nil {
		t.Fatalf("Migrate after rollback: %v", err)
	}
	if applied != store.SupportedSchemaVersion {
		t.Errorf("applied after recovery = %d, want %d", applied, store.SupportedSchemaVersion)
	}
	v, err = s.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion after recovery: %v", err)
	}
	if v != store.SupportedSchemaVersion {
		t.Errorf("schema_version after recovery = %d, want %d", v, store.SupportedSchemaVersion)
	}
}

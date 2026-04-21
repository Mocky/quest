//go:build integration

package store_test

import (
	"context"
	"database/sql"
	stderrors "errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// readMigrationFile returns the raw SQL of the named embedded migration
// read from disk (relative to the store package's test CWD). Used by
// tests that need to apply a specific subset of migrations rather than
// the full embedded set.
func readMigrationFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

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
// v1-era binary. Bootstrap: apply migration 001 only (so the physical
// schema is truly v1 and accepts a `complete` seed), write the legacy
// rows, then run the full Migrate which replays 002+. The earlier
// shortcut of running all migrations and rolling schema_version back
// broke once 003 added a CHECK on status -- the CHECK would reject the
// seed under a physical v3 schema even though the meta row said v1.
func TestMigrateV2RenamesCompleteStatusRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	v1only := fstest.MapFS{
		"001_initial.sql": &fstest.MapFile{Data: readMigrationFile(t, "001_initial.sql")},
	}
	if _, err := store.MigrateFromFS(context.Background(), s, v1only); err != nil {
		t.Fatalf("v1-only Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
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
	wantVersion := strconv.Itoa(store.SupportedSchemaVersion)
	if version != wantVersion {
		t.Errorf("schema_version = %q, want %q", version, wantVersion)
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

// TestMigrateV3CheckConstraintsRejectInvalidEnums pins that after
// migration 003 the DB itself rejects out-of-enum values on
// tasks.status, dependencies.link_type, and history.action. The
// tasks.type CHECK also landed in 003 but was removed again in 006,
// so the type case is not exercised here. Every case goes through a
// direct `sql` INSERT, not through the Go command handlers, so the
// guarantee is at the storage boundary rather than in handler code
// that a future path could bypass.
func TestMigrateV3CheckConstraintsRejectInvalidEnums(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// Seed one valid task so the FK-bearing side tables (dependencies,
	// history) have a real task_id to point at; the CHECK violations
	// below fire before any FK check would, but using a real ID keeps
	// the test focused on the CHECK outcome.
	if _, err := db.Exec(
		`INSERT INTO tasks(id, title, status, created_at)
		 VALUES ('proj-a1', 'ok', 'open', '2026-04-21T00:00:00Z')`); err != nil {
		t.Fatalf("seed valid task: %v", err)
	}

	cases := []struct {
		name string
		stmt string
		args []any
	}{
		{
			name: "tasks.status out of enum",
			stmt: `INSERT INTO tasks(id, title, status, created_at) VALUES (?,?,?,?)`,
			args: []any{"proj-a2", "bad", "complete", "2026-04-21T00:00:00Z"},
		},
		{
			name: "dependencies.link_type out of enum",
			stmt: `INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES (?,?,?,?)`,
			args: []any{"proj-a1", "proj-a1", "related-to", "2026-04-21T00:00:00Z"},
		},
		{
			name: "history.action out of enum",
			stmt: `INSERT INTO history(task_id, timestamp, action) VALUES (?,?,?)`,
			args: []any{"proj-a1", "2026-04-21T00:00:00Z", "commented"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(tc.stmt, tc.args...)
			if err == nil {
				t.Fatalf("insert succeeded, want CHECK constraint failure")
			}
			if !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Errorf("err = %v, want CHECK constraint failure", err)
			}
		})
	}
}

// TestMigrateV3AcceptsEveryEnumValue pins the inverse: every value the
// spec lists as valid must pass the CHECK on each column. If a future
// migration mistypes an entry in the constraint list (or the spec adds
// a value without a matching migration bump), this test fails loudly
// instead of silently shipping a half-enforced enum.
func TestMigrateV3AcceptsEveryEnumValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	validStatuses := []string{"open", "accepted", "completed", "failed", "cancelled"}
	validLinks := []string{"blocked-by", "caused-by", "discovered-from", "retry-of"}
	validActions := []string{
		"created", "accepted", "completed", "failed", "cancelled",
		"reset", "moved", "note_added", "pr_added", "field_updated",
		"linked", "unlinked", "tagged", "untagged", "handoff_set",
	}

	// Seed one task per status so the dependency / history cases below
	// have a real FK target.
	for i, st := range validStatuses {
		id := "proj-s" + strconv.Itoa(i)
		if _, err := db.Exec(
			`INSERT INTO tasks(id, title, status, created_at) VALUES (?,?,?,?)`,
			id, "status-"+st, st, "2026-04-21T00:00:00Z"); err != nil {
			t.Errorf("status %q rejected: %v", st, err)
		}
	}

	// Use the first seeded task as FK target for dependency / history rows.
	parent := "proj-s0"
	for _, lt := range validLinks {
		if _, err := db.Exec(
			`INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES (?,?,?,?)`,
			parent, parent, lt, "2026-04-21T00:00:00Z"); err != nil {
			t.Errorf("link_type %q rejected: %v", lt, err)
		}
	}
	for _, ac := range validActions {
		if _, err := db.Exec(
			`INSERT INTO history(task_id, timestamp, action) VALUES (?,?,?)`,
			parent, "2026-04-21T00:00:00Z", ac); err != nil {
			t.Errorf("action %q rejected: %v", ac, err)
		}
	}
}

// TestMigrateV3PreservesV2Data pins the forward-only invariant for the
// table-recreation pattern used in 003: rows seeded at v2 (valid under
// the new CHECK constraints) survive the migration intact, including
// side-table FK references, indexes, and the history autoincrement id.
func TestMigrateV3PreservesV2Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Apply only 001 + 002 so the schema is a true v2 (no CHECKs yet)
	// and accepts the seed below without interference from 003.
	v2only := fstest.MapFS{
		"001_initial.sql":                &fstest.MapFile{Data: readMigrationFile(t, "001_initial.sql")},
		"002_rename_status_complete.sql": &fstest.MapFile{Data: readMigrationFile(t, "002_rename_status_complete.sql")},
	}
	if _, err := store.MigrateFromFS(context.Background(), s, v2only); err != nil {
		t.Fatalf("v2-only Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO tasks(id, title, status, type, role, tier, created_at) VALUES
			('proj-p1', 'parent', 'open',      'task', 'coder', 'T3', '2026-04-21T00:00:00Z'),
			('proj-p2', 'child',  'accepted',  'bug',  'coder', 'T2', '2026-04-21T00:01:00Z');
		INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES
			('proj-p2', 'proj-p1', 'blocked-by',      '2026-04-21T00:02:00Z'),
			('proj-p2', 'proj-p1', 'caused-by',       '2026-04-21T00:02:00Z'),
			('proj-p2', 'proj-p1', 'discovered-from', '2026-04-21T00:02:00Z'),
			('proj-p2', 'proj-p1', 'retry-of',        '2026-04-21T00:02:00Z');
		INSERT INTO history(task_id, timestamp, action, payload) VALUES
			('proj-p1', '2026-04-21T00:00:00Z', 'created',       '{}'),
			('proj-p2', '2026-04-21T00:01:00Z', 'created',       '{}'),
			('proj-p2', '2026-04-21T00:01:30Z', 'accepted',      '{}'),
			('proj-p2', '2026-04-21T00:01:40Z', 'field_updated', '{"fields":{"tier":{"from":"T3","to":"T2"}}}'),
			('proj-p1', '2026-04-21T00:02:00Z', 'tagged',        '{"tag":"code-review"}');
	`); err != nil {
		t.Fatalf("seed v2 rows: %v", err)
	}
	_ = db.Close()

	// Run full Migrate: stored version is 2; migrations 003 and above
	// replay. Assert the count matches the remaining head distance so the
	// test tolerates future migration bumps without another edit.
	applied, err := store.Migrate(context.Background(), s)
	if err != nil {
		t.Fatalf("Migrate to head: %v", err)
	}
	if want := store.SupportedSchemaVersion - 2; applied != want {
		t.Fatalf("applied = %d, want %d (every migration from 003 up replays)", applied, want)
	}

	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var taskCount, depCount, historyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 2 {
		t.Errorf("tasks count = %d, want 2", taskCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&depCount); err != nil {
		t.Fatalf("count dependencies: %v", err)
	}
	if depCount != 4 {
		t.Errorf("dependencies count = %d, want 4", depCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 5 {
		t.Errorf("history count = %d, want 5", historyCount)
	}

	// Spot-check that the indexes survived the recreation. Listing the
	// index names via sqlite_master is cheaper than exercising queries.
	wantIndexes := []string{
		"idx_tasks_parent", "idx_tasks_status", "idx_tasks_status_role",
		"idx_dependencies_target",
		"idx_history_task_timestamp", "idx_history_timestamp",
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite_master index query: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		have[n] = true
	}
	for _, name := range wantIndexes {
		if !have[name] {
			t.Errorf("missing index %q after v3 (got %v)", name, sortedKeys(have))
		}
	}

	// Parent/child FK survives the tasks recreation -- flip-update the
	// parent id and rely on ON UPDATE CASCADE to rewrite the child's
	// parent pointer. If the FK were dropped by the rename, the child
	// would end up orphaned.
	if _, err := db.Exec(`UPDATE tasks SET id = 'proj-p1x' WHERE id = 'proj-p1'`); err != nil {
		t.Fatalf("rename parent: %v", err)
	}
	var depTarget string
	if err := db.QueryRow(`SELECT target_id FROM dependencies WHERE link_type = 'blocked-by'`).Scan(&depTarget); err != nil {
		t.Fatalf("read dependency after rename: %v", err)
	}
	if depTarget != "proj-p1x" {
		t.Errorf("dependency target_id = %q after CASCADE, want 'proj-p1x'", depTarget)
	}
}

// TestMigrateV4AddsSeverityColumn pins migration 004: a fresh DB landed
// at head has a nullable severity column on tasks, and the column
// defaults to NULL for newly-inserted rows that do not set it.
func TestMigrateV4AddsSeverityColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// PRAGMA table_info confirms the column is present and nullable.
	rows, err := db.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	var (
		found      bool
		nullable   bool
		columnType string
	)
	for rows.Next() {
		var (
			cid     int
			name    string
			colType string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "severity" {
			found = true
			nullable = notNull == 0
			columnType = colType
		}
	}
	if !found {
		t.Fatalf("severity column missing from tasks")
	}
	if !nullable {
		t.Errorf("severity column is NOT NULL, want nullable")
	}
	if columnType != "TEXT" {
		t.Errorf("severity column type = %q, want TEXT", columnType)
	}

	// Insert a row without specifying severity; confirm it lands as NULL.
	if _, err := db.Exec(
		`INSERT INTO tasks(id, title, created_at) VALUES ('proj-a1', 'Alpha', '2026-04-21T00:00:00Z')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var sevIsNull bool
	if err := db.QueryRow(`SELECT severity IS NULL FROM tasks WHERE id = 'proj-a1'`).Scan(&sevIsNull); err != nil {
		t.Fatalf("query severity: %v", err)
	}
	if !sevIsNull {
		t.Errorf("severity for unset row is not NULL")
	}
}

// TestMigrateV4SeverityCheckConstraint pins that after migration 004
// the DB itself rejects out-of-enum severity values, mirroring the
// precedent set by 003 for status/type/link_type/action.
func TestMigrateV4SeverityCheckConstraint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// Every spec-valid severity is accepted, plus NULL (unset).
	valid := []string{"critical", "high", "medium", "low"}
	for i, sev := range valid {
		id := "proj-v" + strconv.Itoa(i)
		if _, err := db.Exec(
			`INSERT INTO tasks(id, title, severity, created_at) VALUES (?,?,?,?)`,
			id, "v-"+sev, sev, "2026-04-21T00:00:00Z"); err != nil {
			t.Errorf("severity %q rejected: %v", sev, err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO tasks(id, title, created_at) VALUES ('proj-vnull', 'null', '2026-04-21T00:00:00Z')`); err != nil {
		t.Errorf("NULL severity rejected: %v", err)
	}

	// Out-of-enum values and wrong casing are rejected at SQL boundary.
	bad := []string{"CRITICAL", "Critical", "urgent", "trivial", ""}
	for i, sev := range bad {
		id := "proj-b" + strconv.Itoa(i)
		_, err := db.Exec(
			`INSERT INTO tasks(id, title, severity, created_at) VALUES (?,?,?,?)`,
			id, "b-"+sev, sev, "2026-04-21T00:00:00Z")
		if err == nil {
			t.Errorf("severity %q accepted, want CHECK failure", sev)
			continue
		}
		if !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("severity %q: err = %v, want CHECK failure", sev, err)
		}
	}
}

// TestMigrateV4PreservesV3Data pins the forward-only invariant for
// migration 004: rows seeded at v3 survive the table recreation, every
// index returns, the FK to parent still CASCADEs on rename, and the
// severity column is left NULL for every pre-migration row.
func TestMigrateV4PreservesV3Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Apply 001..003 so the schema is a true v3 (no severity column yet).
	v3only := fstest.MapFS{
		"001_initial.sql":                &fstest.MapFile{Data: readMigrationFile(t, "001_initial.sql")},
		"002_rename_status_complete.sql": &fstest.MapFile{Data: readMigrationFile(t, "002_rename_status_complete.sql")},
		"003_enum_check_constraints.sql": &fstest.MapFile{Data: readMigrationFile(t, "003_enum_check_constraints.sql")},
	}
	if _, err := store.MigrateFromFS(context.Background(), s, v3only); err != nil {
		t.Fatalf("v3-only Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO tasks(id, title, status, type, role, tier, created_at) VALUES
			('proj-p1', 'parent', 'open',     'task', 'coder', 'T3', '2026-04-21T00:00:00Z'),
			('proj-p2', 'child',  'accepted', 'bug',  'coder', 'T2', '2026-04-21T00:01:00Z');
		INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES
			('proj-p2', 'proj-p1', 'blocked-by', '2026-04-21T00:02:00Z');
		INSERT INTO history(task_id, timestamp, action, payload) VALUES
			('proj-p1', '2026-04-21T00:00:00Z', 'created', '{}'),
			('proj-p2', '2026-04-21T00:01:00Z', 'created', '{}');
	`); err != nil {
		t.Fatalf("seed v3 rows: %v", err)
	}
	_ = db.Close()

	applied, err := store.Migrate(context.Background(), s)
	if err != nil {
		t.Fatalf("Migrate forward from v3: %v", err)
	}
	if applied != store.SupportedSchemaVersion-3 {
		t.Fatalf("applied = %d, want %d (replays 004..head)", applied, store.SupportedSchemaVersion-3)
	}

	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var taskCount, depCount, historyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 2 {
		t.Errorf("tasks count = %d, want 2", taskCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&depCount); err != nil {
		t.Fatalf("count dependencies: %v", err)
	}
	if depCount != 1 {
		t.Errorf("dependencies count = %d, want 1", depCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 2 {
		t.Errorf("history count = %d, want 2", historyCount)
	}

	// Every pre-migration row has severity=NULL.
	var nullSeverities int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE severity IS NULL`).Scan(&nullSeverities); err != nil {
		t.Fatalf("count null severities: %v", err)
	}
	if nullSeverities != 2 {
		t.Errorf("null severities = %d, want 2", nullSeverities)
	}

	// Indexes present after recreation.
	wantIndexes := []string{
		"idx_tasks_parent", "idx_tasks_status", "idx_tasks_status_role",
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite_master index query: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		have[n] = true
	}
	for _, name := range wantIndexes {
		if !have[name] {
			t.Errorf("missing index %q after v4 (got %v)", name, sortedKeys(have))
		}
	}

	// Parent FK still CASCADEs after recreation.
	if _, err := db.Exec(`UPDATE tasks SET id = 'proj-p1x' WHERE id = 'proj-p1'`); err != nil {
		t.Fatalf("rename parent: %v", err)
	}
	var depTarget string
	if err := db.QueryRow(`SELECT target_id FROM dependencies WHERE link_type = 'blocked-by'`).Scan(&depTarget); err != nil {
		t.Fatalf("read dependency after rename: %v", err)
	}
	if depTarget != "proj-p1x" {
		t.Errorf("dependency target_id = %q after CASCADE, want 'proj-p1x'", depTarget)
	}

	// Status CHECK constraint from v3 survives. (The type CHECK also
	// landed in v3 but was removed in v6 together with the column.)
	if _, err := db.Exec(
		`INSERT INTO tasks(id, title, status, created_at) VALUES ('proj-bad', 'bad', 'complete', '2026-04-21T00:00:00Z')`); err == nil {
		t.Errorf("insert with status='complete' accepted after v4, want CHECK failure (v3 constraint must survive)")
	}
}

// TestMigrateV6DropsTypeColumnAndPreservesBugAsTag pins migration 006:
// the tasks.type column is dropped, and every pre-migration row with
// type='bug' gains a `bug` tag via INSERT OR IGNORE so pre-existing bug
// tags are not duplicated. The test seeds at v5 (the schema just before
// 006) and then migrates to head, mirroring the v3→v4 and v2→v3 pinning
// tests above.
func TestMigrateV6DropsTypeColumnAndPreservesBugAsTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Apply 001..005 only so the seed below still has a `type` column
	// to populate. fstest.MapFS lets the test ship its own truncated
	// migration set without touching the embedded FS.
	v5only := fstest.MapFS{
		"001_initial.sql":                &fstest.MapFile{Data: readMigrationFile(t, "001_initial.sql")},
		"002_rename_status_complete.sql": &fstest.MapFile{Data: readMigrationFile(t, "002_rename_status_complete.sql")},
		"003_enum_check_constraints.sql": &fstest.MapFile{Data: readMigrationFile(t, "003_enum_check_constraints.sql")},
		"004_severity.sql":               &fstest.MapFile{Data: readMigrationFile(t, "004_severity.sql")},
		"005_commits.sql":                &fstest.MapFile{Data: readMigrationFile(t, "005_commits.sql")},
	}
	if _, err := store.MigrateFromFS(context.Background(), s, v5only); err != nil {
		t.Fatalf("v5-only Migrate: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// proj-b1 has type='bug' and no existing bug tag — expect one on the
	// far side. proj-b2 has type='bug' AND an existing bug tag — the
	// INSERT OR IGNORE must not duplicate the row. proj-t1 has
	// type='task' — it must come through tag-less (or only with its
	// other tags) and unaffected. proj-b3 has type='bug' with an
	// unrelated tag so we can check ordering and coexistence.
	if _, err := db.Exec(`
		INSERT INTO tasks(id, title, status, type, created_at) VALUES
			('proj-b1', 'bug one',      'open',     'bug',  '2026-04-21T00:00:00Z'),
			('proj-b2', 'bug two',      'accepted', 'bug',  '2026-04-21T00:01:00Z'),
			('proj-b3', 'bug three',    'open',     'bug',  '2026-04-21T00:02:00Z'),
			('proj-t1', 'plain task',   'open',     'task', '2026-04-21T00:03:00Z');
		INSERT INTO tags(task_id, tag) VALUES
			('proj-b2', 'bug'),
			('proj-b3', 'auth'),
			('proj-t1', 'go');
	`); err != nil {
		t.Fatalf("seed v5 rows: %v", err)
	}
	_ = db.Close()

	applied, err := store.Migrate(context.Background(), s)
	if err != nil {
		t.Fatalf("Migrate forward from v5: %v", err)
	}
	if applied != store.SupportedSchemaVersion-5 {
		t.Fatalf("applied = %d, want %d (replays 006..head)", applied, store.SupportedSchemaVersion-5)
	}

	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	// Schema version lands at head.
	var version string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("select schema_version: %v", err)
	}
	if version != strconv.Itoa(store.SupportedSchemaVersion) {
		t.Errorf("schema_version = %q, want %q", version, strconv.Itoa(store.SupportedSchemaVersion))
	}

	// The type column is gone from tasks.
	rows, err := db.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			colType string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "type" {
			t.Errorf("type column present in tasks after v6, want dropped")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}

	// proj-b1 picks up a bug tag (transform case).
	wantTags := map[string][]string{
		"proj-b1": {"bug"},
		"proj-b2": {"bug"},
		"proj-b3": {"auth", "bug"},
		"proj-t1": {"go"},
	}
	for id, want := range wantTags {
		tagRows, err := db.Query(`SELECT tag FROM tags WHERE task_id = ? ORDER BY tag`, id)
		if err != nil {
			t.Fatalf("select tags for %s: %v", id, err)
		}
		var got []string
		for tagRows.Next() {
			var tag string
			if err := tagRows.Scan(&tag); err != nil {
				tagRows.Close()
				t.Fatalf("scan tag: %v", err)
			}
			got = append(got, tag)
		}
		tagRows.Close()
		if len(got) != len(want) {
			t.Errorf("tags for %s = %v, want %v", id, got, want)
			continue
		}
		for i, tag := range want {
			if got[i] != tag {
				t.Errorf("tags for %s = %v, want %v", id, got, want)
				break
			}
		}
	}

	// Idempotency: proj-b2 had a bug tag pre-migration and the migration
	// inserted-or-ignored another. The tags table PK on (task_id, tag)
	// would reject a straight INSERT, and the transform uses OR IGNORE,
	// so exactly one row for ('proj-b2','bug') must remain.
	var b2BugCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tags WHERE task_id = 'proj-b2' AND tag = 'bug'`).Scan(&b2BugCount); err != nil {
		t.Fatalf("count b2 bug tags: %v", err)
	}
	if b2BugCount != 1 {
		t.Errorf("proj-b2 bug tag count = %d, want 1 (INSERT OR IGNORE must dedup)", b2BugCount)
	}

	// proj-t1 (non-bug) did not gain a bug tag.
	var t1BugCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tags WHERE task_id = 'proj-t1' AND tag = 'bug'`).Scan(&t1BugCount); err != nil {
		t.Fatalf("count t1 bug tags: %v", err)
	}
	if t1BugCount != 0 {
		t.Errorf("proj-t1 (non-bug) gained a bug tag, want none")
	}

	// Every pre-migration row still exists.
	var taskCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 4 {
		t.Errorf("tasks count = %d, want 4", taskCount)
	}

	// Indexes return after the CREATE/INSERT/DROP/RENAME.
	wantIndexes := []string{
		"idx_tasks_parent", "idx_tasks_status", "idx_tasks_status_role",
	}
	idxRows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite_master index query: %v", err)
	}
	defer idxRows.Close()
	have := map[string]bool{}
	for idxRows.Next() {
		var n string
		if err := idxRows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		have[n] = true
	}
	for _, name := range wantIndexes {
		if !have[name] {
			t.Errorf("missing index %q after v6 (got %v)", name, sortedKeys(have))
		}
	}
}

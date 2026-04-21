//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mocky/quest/internal/cli"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
	"github.com/mocky/quest/internal/testutil"
)

// migrateSpanExporter installs an in-memory tracer + meter and returns
// both. Tests assert spans via tracetest.SpanStubs and metrics via the
// captured ManualReader. The telemetry package's enabled() flag is
// flipped on so InstrumentedStore wraps the store and emits the
// store-tx span (a side benefit for the assertion that init produces
// quest.db.migrate as a child of the command span).
func migrateSpanExporter(t *testing.T) (*tracetest.InMemoryExporter, *testutil.CapturingMeter) {
	t.Helper()
	exp := testutil.NewCapturingTracer(t)
	meter := testutil.NewCapturingMeter(t)
	telemetry.MarkEnabledForTest()
	t.Cleanup(telemetry.MarkDisabledForTest)
	telemetry.InitInstrumentsForTest()
	return exp, meter
}

// TestInitProducesMigrateSpanAsChild covers the §8.8 carve-out: init
// runs the migration from inside its handler so quest.db.migrate is a
// CHILD of execute_tool quest.init, not a sibling. Counter increments
// exactly once.
func TestInitProducesMigrateSpanAsChild(t *testing.T) {
	exp, meter := migrateSpanExporter(t)
	root := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(orig)

	cfg := config.Config{
		Log:    config.LogConfig{Level: "warn", OTELLevel: "info"},
		Output: config.OutputConfig{},
	}
	exit, _, errb := runExecute([]string{"init", "--prefix", "proj"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d; stderr=%s", exit, errb)
	}

	spans := exp.GetSpans()
	var initSpan, migrateSpan *tracetest.SpanStub
	for i := range spans {
		switch spans[i].Name {
		case "execute_tool quest.init":
			initSpan = &spans[i]
		case "quest.db.migrate":
			migrateSpan = &spans[i]
		}
	}
	if initSpan == nil {
		t.Fatalf("init span missing; got %v", spanNames(spans))
	}
	if migrateSpan == nil {
		t.Fatalf("migrate span missing; got %v", spanNames(spans))
	}
	if migrateSpan.Parent.SpanID() != initSpan.SpanContext.SpanID() {
		t.Errorf("migrate span parent = %s; want init span %s",
			migrateSpan.Parent.SpanID(), initSpan.SpanContext.SpanID())
	}

	if got := schemaMigrationCount(t, meter); got != 1 {
		t.Errorf("dept.quest.schema.migrations count = %d; want 1", got)
	}
}

// TestDispatcherPathMigrateSpanIsSibling covers the §8.8 main path:
// for any workspace-bound command other than init, quest.db.migrate is
// a sibling of execute_tool quest.<command>, not a child. Both share
// the same parent (the inbound TRACEPARENT-derived context — here, a
// fresh root since no TRACEPARENT was set). The dispatcher gates on
// stored_version < SupportedSchemaVersion before calling MigrateSpan.
func TestDispatcherPathMigrateSpanIsSibling(t *testing.T) {
	exp, meter := migrateSpanExporter(t)
	cfg := setupWorkspace(t, "proj", "planner")

	// Run any workspace-bound command. The first invocation triggers
	// the dispatcher's migration path (stored=0 < supported=1).
	exit, _, errb := runExecute([]string{"list"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d; stderr=%s", exit, errb)
	}

	spans := exp.GetSpans()
	var cmdSpan, migrateSpan *tracetest.SpanStub
	for i := range spans {
		switch spans[i].Name {
		case "execute_tool quest.list":
			cmdSpan = &spans[i]
		case "quest.db.migrate":
			migrateSpan = &spans[i]
		}
	}
	if cmdSpan == nil || migrateSpan == nil {
		t.Fatalf("missing spans; got %v", spanNames(spans))
	}
	// Sibling: same parent span context (which is invalid here since
	// there is no inbound TRACEPARENT, so both have a zero parent).
	if migrateSpan.Parent.SpanID() != cmdSpan.Parent.SpanID() {
		t.Errorf("migrate parent != cmd parent — span tree shape broke")
	}
	// Migrate span must not be a child of the command span.
	if migrateSpan.Parent.SpanID() == cmdSpan.SpanContext.SpanID() {
		t.Errorf("migrate span is a child of cmd span; want sibling")
	}
	if got := schemaMigrationCount(t, meter); got != 1 {
		t.Errorf("dept.quest.schema.migrations count = %d; want 1", got)
	}
}

// TestDispatcherWritesPreMigrationSnapshot pins spec §Storage > Pre-
// migration snapshot: when the dispatcher migrates a workspace from
// schema_version > 0 to SupportedSchemaVersion, it first writes a
// transaction-consistent snapshot to .quest/backups/pre-v{N}-*.db via
// the online backup API. Seeds the workspace at schema_version 1
// (after a full init to 2, then a manual downgrade of meta) so the
// second dispatcher call exercises the real migration-plus-snapshot
// path.
func TestDispatcherWritesPreMigrationSnapshot(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "planner")

	// First invocation migrates 0→head (no snapshot — from == 0 is the
	// fresh-init carve-out).
	if exit, _, errb := runExecute([]string{"list"}, cfg); exit != 0 {
		t.Fatalf("prime list: exit=%d stderr=%s", exit, errb)
	}

	// Manually regress meta.schema_version to head-1 so the next
	// dispatcher call migrates (head-1)→head and takes a pre-migration
	// snapshot. Also drop the table that migration `head` creates so
	// the replay matches what a real v{head-1} workspace would carry.
	db, err := sql.Open("sqlite", "file:"+cfg.Workspace.DBPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	regress := strconv.Itoa(store.SupportedSchemaVersion - 1)
	if _, err := db.Exec(`UPDATE meta SET value = ? WHERE key = 'schema_version'`, regress); err != nil {
		t.Fatalf("regress meta: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE commits`); err != nil {
		t.Fatalf("drop commits: %v", err)
	}
	_ = db.Close()

	if exit, _, errb := runExecute([]string{"list"}, cfg); exit != 0 {
		t.Fatalf("second list: exit=%d stderr=%s", exit, errb)
	}

	// Find the snapshot file. The snapshot name carries the target
	// schema version (see store.WritePreMigrationSnapshot) so this
	// tracks SupportedSchemaVersion automatically.
	snapPattern := fmt.Sprintf("pre-v%d-*.db", store.SupportedSchemaVersion)
	matches, err := filepath.Glob(filepath.Join(cfg.Workspace.Root, ".quest", "backups", snapPattern))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("%s snapshot count = %d, want 1; matches=%v", snapPattern, len(matches), matches)
	}

	// The snapshot's schema_version should equal the pre-migration
	// state (head-1).
	snap, err := sql.Open("sqlite", "file:"+matches[0])
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	var v string
	if err := snap.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatalf("read snapshot schema_version: %v", err)
	}
	if v != regress {
		t.Errorf("snapshot schema_version = %q, want %q (pre-migration state)", v, regress)
	}

	// Live DB should be at 2 afterwards.
	live, err := sql.Open("sqlite", "file:"+cfg.Workspace.DBPath)
	if err != nil {
		t.Fatalf("reopen live: %v", err)
	}
	defer live.Close()
	if err := live.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatalf("read live schema_version: %v", err)
	}
	wantLive := strconv.Itoa(store.SupportedSchemaVersion)
	if v != wantLive {
		t.Errorf("live schema_version = %q, want %q", v, wantLive)
	}
}

// TestPreMigrationSnapshotFailureAbortsMigration is the load-bearing
// safety invariant: if the pre-migration snapshot cannot be written,
// the migration MUST NOT run. Blocks MkdirAll by putting a regular
// file where .quest/backups/ would go, drives the dispatcher, asserts
// exit 1 and that the live DB's schema_version is unchanged.
func TestPreMigrationSnapshotFailureAbortsMigration(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "planner")

	// Prime to schema_version 2.
	if exit, _, errb := runExecute([]string{"list"}, cfg); exit != 0 {
		t.Fatalf("prime list: exit=%d stderr=%s", exit, errb)
	}
	// Regress to schema_version 1.
	db, err := sql.Open("sqlite", "file:"+cfg.Workspace.DBPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '1' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("regress meta: %v", err)
	}
	_ = db.Close()

	// Block .quest/backups/ by placing a non-directory at that path.
	blocker := filepath.Join(cfg.Workspace.Root, ".quest", "backups")
	if err := os.WriteFile(blocker, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}

	exit, _, stderr := runExecute([]string{"list"}, cfg)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1 (general_failure); stderr=%q", exit, stderr)
	}
	if !strings.Contains(stderr, "pre-migration snapshot failed") {
		t.Errorf("stderr missing pre-migration message: %q", stderr)
	}

	// Live DB must still be at schema_version 1 — the migration did
	// not run.
	live, err := sql.Open("sqlite", "file:"+cfg.Workspace.DBPath)
	if err != nil {
		t.Fatalf("reopen live: %v", err)
	}
	defer live.Close()
	var v string
	if err := live.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatalf("read live schema_version: %v", err)
	}
	if v != "1" {
		t.Errorf("live schema_version = %q, want 1 (migration should not have run)", v)
	}
}

// TestFreshInitDoesNotWriteSnapshot pins the fresh-init carve-out
// from quest-spec.md §Storage > Pre-migration snapshot: when
// from == 0 (a fresh init), no pre-migration snapshot is taken
// because the prior-version file has no recoverable content.
func TestFreshInitDoesNotWriteSnapshot(t *testing.T) {
	root := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(orig)

	cfg := config.Config{
		Log:    config.LogConfig{Level: "warn", OTELLevel: "info"},
		Output: config.OutputConfig{},
	}
	if exit, _, errb := runExecute([]string{"init", "--prefix", "proj"}, cfg); exit != 0 {
		t.Fatalf("init exit=%d stderr=%s", exit, errb)
	}

	// Also run a dispatcher-path command to exercise the other call
	// site; the first workspace-bound command after init finds
	// schema_version already at SupportedSchemaVersion, so no migration
	// path is entered.
	wrkCfg := config.Config{
		Workspace: config.WorkspaceConfig{
			Root:          root,
			DBPath:        filepath.Join(root, ".quest", "quest.db"),
			IDPrefix:      "proj",
			ElevatedRoles: []string{"planner"},
		},
		Agent:  config.AgentConfig{Role: "planner"},
		Log:    config.LogConfig{Level: "warn", OTELLevel: "info"},
		Output: config.OutputConfig{},
	}
	if exit, _, errb := runExecute([]string{"list"}, wrkCfg); exit != 0 {
		t.Fatalf("list after init exit=%d stderr=%s", exit, errb)
	}

	backupsDir := filepath.Join(root, ".quest", "backups")
	info, err := os.Stat(backupsDir)
	switch {
	case os.IsNotExist(err):
		// Correct — directory should not exist on fresh init.
	case err != nil:
		t.Fatalf("stat .quest/backups: %v", err)
	default:
		// Allowed only if empty.
		if !info.IsDir() {
			t.Fatalf(".quest/backups is not a directory")
		}
		entries, err := os.ReadDir(backupsDir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("fresh init produced %d backup files; want 0", len(entries))
		}
	}
}

// TestUpToDateDBNoMigrateSpan covers the H1 gate: when the stored
// schema_version equals SupportedSchemaVersion, the dispatcher does
// not call MigrateSpan. Exporter sees only the command span; the
// schema-migrations counter does not increment.
func TestUpToDateDBNoMigrateSpan(t *testing.T) {
	exp, meter := migrateSpanExporter(t)
	cfg := setupWorkspace(t, "proj", "planner")
	bare, err := store.Open(cfg.Workspace.DBPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := store.Migrate(context.Background(), bare); err != nil {
		bare.Close()
		t.Fatalf("Migrate: %v", err)
	}
	bare.Close()

	exit, _, errb := runExecute([]string{"list"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d; stderr=%s", exit, errb)
	}
	for _, s := range exp.GetSpans() {
		if s.Name == "quest.db.migrate" {
			t.Errorf("found unexpected quest.db.migrate span on up-to-date DB")
		}
	}
	if got := schemaMigrationCount(t, meter); got != 0 {
		t.Errorf("dept.quest.schema.migrations count = %d; want 0 (no migration ran)", got)
	}
}

// schemaMigrationCount sums every dept.quest.schema.migrations data
// point captured by the meter.
func schemaMigrationCount(t *testing.T, m *testutil.CapturingMeter) int64 {
	t.Helper()
	rm := m.Collect(context.Background())
	total := int64(0)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "dept.quest.schema.migrations" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
		}
	}
	return total
}

func spanNames(spans tracetest.SpanStubs) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Name
	}
	return out
}

var _ = bytes.Buffer{}
var _ = filepath.Join
var _ = strings.HasPrefix
var _ = cli.Execute

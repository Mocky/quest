//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
		Output: config.OutputConfig{Format: "json"},
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

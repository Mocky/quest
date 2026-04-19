package telemetry_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

func TestStubsDoNotPanic(t *testing.T) {
	ctx := context.Background()

	bridge, shutdown, err := telemetry.Setup(ctx, telemetry.Config{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if bridge != nil {
		t.Errorf("Phase 2 Setup bridge = %v; want nil", bridge)
	}
	if err := shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}

	got := telemetry.ExtractTraceFromConfig(ctx, "", "")
	if got != ctx {
		t.Errorf("ExtractTraceFromConfig did not return ctx unchanged")
	}

	// Phase 12 made CommandSpan a real tracer.Start call; the no-op
	// global tracer still returns a non-recording span and a context
	// derived from the parent so this exercises the disabled path.
	_, span := telemetry.CommandSpan(ctx, "version", false)
	span.End()

	if err := telemetry.WrapCommand(ctx, "version", func(context.Context) error { return nil }); err != nil {
		t.Errorf("WrapCommand: %v", err)
	}

	telemetry.GateSpan(ctx, "", true)

	_, end := telemetry.MigrateSpan(ctx, 0, 1)
	end(1, nil)

	_, endStore := telemetry.StoreSpan(ctx, "quest.store.traverse")
	endStore(nil)

	trID, spID, ok := telemetry.TraceIDsFromContext(ctx)
	if ok || trID != "" || spID != "" {
		t.Errorf("TraceIDsFromContext = (%q,%q,%v); want empty/false", trID, spID, ok)
	}

	if telemetry.CaptureContentEnabled() {
		t.Errorf("CaptureContentEnabled = true; want false before Setup flips it")
	}

	var s store.Store
	if got := telemetry.WrapStore(s); got != s {
		t.Errorf("WrapStore did not return argument unchanged")
	}

	telemetry.RecordTaskContext(ctx, "proj-1", "T2", "task")
	telemetry.RecordHandlerError(ctx, io.EOF)
	// RecordDispatchError in Phase 4 emits stderr + returns the mapped
	// exit code so cli.Execute's early-return paths work today; the OTEL
	// span/counter wiring is deferred to Task 12.5. io.EOF does not wrap
	// a quest sentinel, so it maps to general_failure (exit 1).
	if code := telemetry.RecordDispatchError(ctx, io.EOF, io.Discard); code != 1 {
		t.Errorf("RecordDispatchError(io.EOF) = %d; want 1 (general_failure)", code)
	}
	if code := telemetry.RecordDispatchError(ctx, nil, io.Discard); code != 0 {
		t.Errorf("RecordDispatchError(nil) = %d; want 0", code)
	}
	telemetry.RecordPreconditionFailed(ctx, "children_terminal", []string{"a", "b"})
	telemetry.RecordCycleDetected(ctx, []string{"a", "b", "a"})
	telemetry.RecordTerminalState(ctx, "proj-1", "T2", "coder", "complete")
	telemetry.RecordTaskCreated(ctx, "proj-1", "T2", "coder", "task")
	telemetry.RecordStatusTransition(ctx, "proj-1", "open", "accepted")
	telemetry.RecordLinkAdded(ctx, "a", "b", "blocked-by")
	telemetry.RecordLinkRemoved(ctx, "a", "b", "blocked-by")
	telemetry.RecordBatchOutcome(ctx, 3, 0, "ok")
	telemetry.RecordBatchError(ctx, "parse", "missing_field", "title", "ref1", 5)
	telemetry.RecordMoveOutcome(ctx, "proj-a1.3", "proj-b2.1", 4, 5)
	telemetry.RecordCancelOutcome(ctx, "proj-1", true, 3, 1)
	telemetry.RecordContentReason(ctx, "superseded by proj-b2")
	telemetry.RecordQueryResult(ctx, "list", 12)
	telemetry.RecordGraphResult(ctx, 10, 50)
}

func TestTruncatePreservesUTF8Boundary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"empty", "", 10, ""},
		{"within", "hello", 10, "hello"},
		{"ascii cut", "hello world", 5, "hello"},
		{"multi-byte cut backs off", "héllo", 2, "h"},
		{"non-positive max", "hello", 0, "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := telemetry.Truncate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q; want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// TestNoOTELImportsOutsideTelemetry is the Task 2.3 grep tripwire:
// a handler that accidentally imports `go.opentelemetry.io` fails the
// build. Walks every non-test .go file under internal/ and cmd/ and
// rejects any match outside internal/telemetry/. internal/testutil/ is
// also exempt — its capturing-tracer / capturing-meter helpers must
// import OTEL to assert on emitted signals (Phase 12 plan §Shared
// `tracetest` helper); the package is test-only per its doc.go and is
// never linked into production code paths.
func TestNoOTELImportsOutsideTelemetry(t *testing.T) {
	root := findRepoRoot(t)
	allowedPrefixes := []string{
		filepath.Join(root, "internal", "telemetry") + string(filepath.Separator),
		filepath.Join(root, "internal", "testutil") + string(filepath.Separator),
	}
	bad := []string{}
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			for _, allowed := range allowedPrefixes {
				if strings.HasPrefix(p, allowed) {
					return nil
				}
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if strings.Contains(string(b), "go.opentelemetry.io") {
				rel, _ := filepath.Rel(root, p)
				bad = append(bad, rel)
			}
			return nil
		})
	}
	if len(bad) > 0 {
		t.Fatalf("OTEL import found outside internal/telemetry/: %v", bad)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for p := cwd; p != "/"; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
			return p
		}
	}
	t.Fatalf("go.mod not found walking up from %s", cwd)
	return ""
}

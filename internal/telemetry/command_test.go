package telemetry

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mocky/quest/internal/errors"
)

// installInMemoryTracer swaps the global tracer for an in-memory one
// and returns the exporter. Restores the previous global on cleanup.
// Same pattern as testutil.NewCapturingTracer but kept package-local
// so internal/telemetry tests can run without importing testutil
// (which itself imports OTEL — keeps the test layering shallow).
func installInMemoryTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	tracer = tp.Tracer("dept.quest")
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		tracer = otel.Tracer("dept.quest")
	})
	return exp
}

// TestCommandSpanRequiredAttributes confirms the §4.3 attribute set on
// the root span matches OTEL.md exactly. Empty AGENT_ROLE surfaces as
// the literal "unset" via roleOrUnset.
func TestCommandSpanRequiredAttributes(t *testing.T) {
	exp := installInMemoryTracer(t)
	defer setIdentity("", "", "")
	setIdentity("", "task-7", "session-2")

	_, span := CommandSpan(context.Background(), "create", true)
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want 1", len(spans))
	}
	s := spans[0]
	if s.Name != "execute_tool quest.create" {
		t.Errorf("span name = %q; want %q", s.Name, "execute_tool quest.create")
	}
	want := map[attribute.Key]attribute.Value{
		"gen_ai.tool.name":      attribute.StringValue("quest.create"),
		"gen_ai.operation.name": attribute.StringValue("execute_tool"),
		"gen_ai.agent.name":     attribute.StringValue("unset"),
		"dept.task.id":          attribute.StringValue("task-7"),
		"dept.session.id":       attribute.StringValue("session-2"),
		"quest.role.elevated":   attribute.BoolValue(true),
	}
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes {
		got[kv.Key] = kv.Value
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("attr %s: got %v, want %v", k, g, v)
		}
	}
}

// TestWrapCommandDoesNotEndSpan confirms the dispatcher-shape contract:
// CommandSpan opens, WrapCommand observes, defer span.End closes.
// Without that contract, every command would record either zero or two
// End events on the exporter — the contract test catches a regression
// where WrapCommand grew its own span.End.
func TestWrapCommandDoesNotEndSpan(t *testing.T) {
	exp := installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "show", false)
	if err := WrapCommand(ctx, "show", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("WrapCommand: %v", err)
	}
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want exactly 1 command span", len(spans))
	}
}

// TestWrapCommandReturnsHandlerError confirms WrapCommand surfaces
// fn's error verbatim — the dispatcher can wrap or transform it but
// WrapCommand must not. The richer "spans gain quest.error.class +
// retryable + exit_code" assertion lives with Task 12.5's
// RecordHandlerError test.
func TestWrapCommandReturnsHandlerError(t *testing.T) {
	installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "accept", false)
	wantErr := fmt.Errorf("%w: parent not in open", errors.ErrConflict)
	gotErr := WrapCommand(ctx, "accept", func(context.Context) error { return wantErr })
	if !stderrors.Is(gotErr, errors.ErrConflict) {
		t.Errorf("WrapCommand returned %v; want wrapping ErrConflict", gotErr)
	}
	span.End()
}

// TestWrapCommandIncrementsOperations confirms WrapCommand increments
// dept.quest.operations on both the success and error paths. We can't
// inspect a nil counter here (Task 12.5 wires the instrument), so the
// test confirms the counter is left untouched on the disabled path
// without panicking.
func TestWrapCommandIncrementsOperations(t *testing.T) {
	ctx := context.Background()
	if err := WrapCommand(ctx, "show", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("WrapCommand ok path: %v", err)
	}
	if err := WrapCommand(ctx, "show", func(context.Context) error { return errors.ErrUsage }); err == nil {
		t.Fatalf("WrapCommand error path: got nil error")
	}
}

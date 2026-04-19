package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// CommandSpan opens the root `execute_tool quest.<cmd>` span.
// cli.Execute calls it once per invocation and defers span.End()
// (OTEL.md §8.2). In Phase 2 it returns ctx and a non-recording span
// pulled from trace.SpanFromContext — calling End / SetStatus on it is
// valid and cheap. Task 12.1 replaces the body with tracer.Start.
func CommandSpan(ctx context.Context, command string, elevated bool) (context.Context, trace.Span) {
	_ = command
	_ = elevated
	return ctx, trace.SpanFromContext(ctx)
}

// WrapCommand is the dispatcher-owned middleware that runs fn inside
// the already-open command span (picked up via trace.SpanFromContext).
// On a non-nil returned error it applies the §4.4 three-step pattern
// (RecordError + SetStatus + dept.quest.errors counter). It does NOT
// start or end a span — cli.Execute owns that via CommandSpan plus
// defer span.End(). Phase 2 stub simply calls fn. (OTEL.md §8.2.)
func WrapCommand(ctx context.Context, command string, fn func(context.Context) error) error {
	_ = command
	return fn(ctx)
}

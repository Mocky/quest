package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// ValidateSpan opens the parent `quest.validate` span under the
// active command span (OTEL.md §8.5). Used by `quest batch` (and any
// future multi-phase validation) so each phase span shows up as a
// child of a single validate scope rather than directly under the
// command span.
func ValidateSpan(ctx context.Context) (context.Context, func()) {
	ctx, span := tracer.Start(ctx, "quest.validate", trace.WithSpanKind(trace.SpanKindInternal))
	return ctx, func() { span.End() }
}

// BatchPhaseSpan opens a `quest.batch.<phase>` child span under the
// active validate span. The returned ctx carries the phase span so
// RecordBatchError calls inside the closure attach their event to the
// phase span rather than the parent. (OTEL.md §8.5.) phase is one of
// "parse", "reference", "graph", "semantic" — bounded enum, no
// validation here since the batch handler knows the value statically.
func BatchPhaseSpan(ctx context.Context, phase string) (context.Context, func()) {
	ctx, span := tracer.Start(ctx, "quest.batch."+phase, trace.WithSpanKind(trace.SpanKindInternal))
	return ctx, func() { span.End() }
}

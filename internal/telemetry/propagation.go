package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// ExtractTraceFromConfig extracts the W3C trace context from the
// TRACEPARENT/TRACESTATE strings resolved by internal/config/. When
// traceparent is empty it returns ctx unchanged regardless of
// tracestate (OTEL.md §6.2). Setup must register the W3C composite
// propagator before this is called; otherwise the global no-op
// propagator silently swallows the input.
func ExtractTraceFromConfig(ctx context.Context, traceparent, tracestate string) context.Context {
	if traceparent == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	if tracestate != "" {
		carrier["tracestate"] = tracestate
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// TraceIDsFromContext returns the active span's trace and span IDs when
// the ctx carries a recording span. The stderr slog handler uses it to
// enrich records per OBSERVABILITY.md §Correlation Identifiers; ok=false
// signals "no span attached" so the handler omits the fields.
func TraceIDsFromContext(ctx context.Context) (traceID, spanID string, ok bool) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", "", false
	}
	return sc.TraceID().String(), sc.SpanID().String(), true
}

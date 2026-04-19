package telemetry

import "context"

// ExtractTraceFromConfig extracts the W3C trace context from the
// TRACEPARENT/TRACESTATE strings resolved by internal/config/. In
// Phase 2 it returns ctx unchanged; Task 12.1 swaps in the real
// propagator-backed implementation (OTEL.md §6.2 / §7.1).
func ExtractTraceFromConfig(ctx context.Context, traceparent, tracestate string) context.Context {
	_ = traceparent
	_ = tracestate
	return ctx
}

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// GateSpan records the role-gate decision as a `quest.role.gate` child
// span on the active command span (OTEL.md §8.7). cli.Execute calls it
// once per elevated command; `quest update` also calls it from the
// mixed-flag gate so dashboards count both denials. Telemetry is a pure
// observer — the boolean is computed by cli.Execute via
// config.IsElevated; this function never imports internal/config/.
// The span is emitted whether or not the command proceeds: retrospective
// queries care about attempts, not just denials.
func GateSpan(ctx context.Context, agentRole string, allowed bool) {
	_, span := tracer.Start(ctx, "quest.role.gate",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("quest.role.required", "elevated"),
			attribute.String("quest.role.actual", roleOrUnset(agentRole)),
			attribute.Bool("quest.role.allowed", allowed),
		),
	)
	span.End()
}

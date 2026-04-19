package telemetry

import "context"

// GateSpan records the role-gate decision as a `quest.role.gate` child
// span on the active command span (OTEL.md §8.7). cli.Execute calls it
// once per elevated command; `quest update` also calls it from the
// mixed-flag gate so dashboards count both denials. Telemetry is a pure
// observer — the boolean is computed by cli.Execute via
// config.IsElevated. Phase 2 stub is a no-op.
func GateSpan(ctx context.Context, agentRole string, allowed bool) {
	_ = ctx
	_ = agentRole
	_ = allowed
}

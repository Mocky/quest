// Package logging builds the slog default handler for the quest CLI.
// main.run() calls Setup twice — once before telemetry.Setup (pre-bridge)
// and once after, passing the OTEL bridge handler in so OTEL logs
// receive a copy of every slog record (OTEL.md §7.1). Setup is the only
// exported function. Handlers themselves never import OTEL; the bridge
// handler is constructed inside internal/telemetry/ and handed in as an
// opaque slog.Handler. Phase 2 fills in the fan-out handler and adds
// sanitization per OBSERVABILITY.md; Phase 0 wires the minimum default
// handler. See OBSERVABILITY.md and OTEL.md §10.1.
package logging

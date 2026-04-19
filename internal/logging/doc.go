// Package logging builds the slog logger for the quest CLI. main.run()
// calls Setup twice — once before telemetry.Setup returns the OTEL
// bridge (pre-bridge, stderr only) and once after (with the bridge as
// an extra) so OTEL receives every slog record. Setup composes a
// fan-out handler: each child is level-gated independently so stderr
// and the OTEL bridge can run at different thresholds per
// OBSERVABILITY.md §Correlation Identifiers and OTEL.md §3.2. The
// stderr path wraps slog.NewTextHandler with a trace-enrichment layer
// that adds trace_id/span_id via telemetry.TraceIDsFromContext —
// internal/logging/ never imports OTEL directly (OTEL.md §10.1).
// LevelFromString is the single source of level-string parsing;
// config.Validate can use it to reject malformed log-level values.
package logging

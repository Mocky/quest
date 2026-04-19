// Package cli parses quest's global flags, dispatches to command
// handlers, enforces role gating, and owns the per-command telemetry
// span (OTEL.md §8.2). Handlers live in internal/command/ and never
// parse flags or call os.Exit; cli owns both. Exported in Phase 0:
// ParseGlobals (two-field --format / --log-level parser) and Execute
// (the dispatcher). The full role gate, per-command flag parsing,
// CommandSpan/WrapCommand wiring, and slog `quest command start/complete`
// events land in Task 4.2. See quest-spec.md §Commands and
// STANDARDS.md §CLI Surface Versioning.
package cli

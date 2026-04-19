// Package command holds one file per CLI command handler (version.go,
// accept.go, create.go, ...). Handlers live flat here per Task 13.1 so
// contract_test.go sits alongside them. A handler receives a resolved
// config.Config, the remaining args, stdin/stdout/stderr streams, and
// returns an int exit code; it never calls os.Exit, never parses flags
// beyond its own per-command ones, and never reads env vars. Handlers
// emit JSON or text via internal/output/, log via slog, and record
// telemetry via internal/telemetry/ wrappers. Phase 0 ships only
// version.go; the rest land in Phases 5–11. See quest-spec.md
// §Commands.
package command

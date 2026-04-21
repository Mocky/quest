// Package cli parses quest's global flags, dispatches to command
// handlers, enforces role gating, and owns the per-command telemetry
// span (OTEL.md §8.2). Handlers live in internal/command/ and never
// parse flags or call os.Exit; cli owns both. Exports:
//
//   - ParseGlobals — position-independent --text / --log-level parser.
//   - Execute       — the dispatcher (command identification, role gate,
//     workspace + config validation, store open + migrate, per-command
//     span, handler dispatch, panic recovery).
//   - Suggest       — Levenshtein "did you mean" helper for unknown
//     commands and unknown enum filter values.
//   - Handler       — the signature every command handler honors.
//
// The package does not import OTEL (the tripwire in
// internal/telemetry/telemetry_test.go enforces this); it uses the
// helpers in internal/telemetry/ for span / gate / migrate / dispatch-
// error work. See quest-spec.md §Commands and STANDARDS.md §CLI
// Surface Versioning.
package cli

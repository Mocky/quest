// Package testutil aggregates shared test helpers. Current surface:
//
//   - AssertSchema(t, got []byte, required []string) — unmarshals got
//     as a JSON object and fails t if any required key is absent.
//     Used by Phase 6+ contract tests that assert every spec-pinned
//     JSON field is present (even as null / [] / {}).
//   - AssertJSONKeyOrder(t, got []byte, want []string) — decodes got
//     and asserts the top-level keys appear in want's relative order.
//     Catches regressions that swap an ordered output struct for a
//     plain map (which serializes alphabetically).
//   - NewCapturingTracer / NewCapturingMeter / NewCapturingLogger —
//     install in-memory OTEL providers and restore globals on
//     t.Cleanup. Used by Phase 12+ telemetry contract tests.
//
// Test-only — never imported by production code. Files here may carry
// `//go:build integration` when they build the CLI binary (TESTING.md).
package testutil

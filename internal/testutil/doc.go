// Package testutil aggregates shared test helpers. Current surface:
//
//   - AssertSchema(t, got []byte, required []string) — unmarshals got
//     as a JSON object and fails t if any required key is absent.
//     Used by Phase 6+ contract tests that assert every spec-pinned
//     JSON field is present (even as null / [] / {}).
//
// Planned surface (per Task 0.1): NewWorkspace, NewStore, NewFakeStore
// (in-memory Store fake), SeedTask, AssertExitCode, AssertErrorClass,
// AssertJSONKeyOrder, capturing tracer / meter / logger for Phase 13
// observability tests. Test-only — never imported by production code.
// Files here may carry `//go:build integration` when they build the
// CLI binary (TESTING.md).
package testutil

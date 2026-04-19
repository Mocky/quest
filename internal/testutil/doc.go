// Package testutil aggregates shared test helpers. Planned surface
// (per Task 0.1): NewWorkspace(t), NewStore(t), NewFakeStore(t) (the
// in-memory Store fake for unit tests without SQLite), SeedTask(t,
// store, Task), AssertExitCode(t, got, want int), AssertErrorClass(t,
// err, wantClass string), AssertJSONKeyOrder(t, got []byte, want
// []string), AssertSchema(t, got []byte, required []string),
// NewCapturingTracer/NewCapturingMeter/NewCapturingLogger (via
// OTEL tracetest / metrictest). Test-only — never imported by
// production code. Files in this package may carry `//go:build
// integration` when they build the CLI binary (TESTING.md).
package testutil

// Package telemetry is the only package in quest that imports
// OpenTelemetry (OTEL.md §10.1). Every other package calls
// telemetry.CommandSpan / WrapCommand / GateSpan / MigrateSpan /
// StoreSpan / WrapStore and the RecordX recorders so OTEL types never
// leak across the boundary. The file layout matches OTEL.md §8.1:
// setup.go, identity.go, propagation.go, command.go, gate.go,
// migrate.go, recorder.go, store.go, validation.go, truncate.go. In
// Phase 2 every function is a no-op shell with the final signatures
// Task 12 fills in — handlers call the real entry points from day one
// so Task 12 is a drop-in replacement. A grep tripwire
// (`grep -rn 'go.opentelemetry.io' internal/ cmd/`) fails the build if
// any package outside this one imports OTEL.
package telemetry

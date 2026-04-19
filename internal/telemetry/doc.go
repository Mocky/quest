// Package telemetry is the only package in quest that imports
// OpenTelemetry (OTEL.md §10.1). Every other package calls
// telemetry.RecordX / telemetry.CommandSpan / telemetry.WrapCommand
// wrappers so the OTEL types never leak across the boundary. Exported
// surface in Phase 0: Config, Setup, ExtractTraceFromConfig — all
// no-op shells that satisfy main.run()'s contract. Phase 2 adds the
// full Record*-style stub inventory for handlers to call; Phase 12
// replaces the no-op Setup with real provider wiring. A grep tripwire
// (Task 2.3) fails the build if any other package imports
// go.opentelemetry.io. See OTEL.md §§7.1, 10.1.
package telemetry

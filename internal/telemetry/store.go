package telemetry

import "github.com/mocky/quest/internal/store"

// WrapStore returns the InstrumentedStore decorator when telemetry is
// enabled and the bare store otherwise (OTEL.md §8.3). The decorator
// type and the enabled() package-private helper land in Task 12.4;
// Phase 2 is a pass-through so cli.Execute (Task 4.2) can call
// telemetry.WrapStore at its single construction site from day one.
func WrapStore(s store.Store) store.Store { return s }

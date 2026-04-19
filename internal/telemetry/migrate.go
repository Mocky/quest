package telemetry

import "context"

// MigrateSpan opens `quest.db.migrate` at span-start with the
// quest.schema.from and quest.schema.to attributes so the backend can
// index on them even if the span is cut short by a migration error
// (OTEL.md §8.8). The returned end(applied, err) closure sets
// quest.schema.applied_count, applies the three-step error pattern on
// error, ends the span, and increments the
// dept.quest.schema.migrations counter. Phase 2 returns ctx and a
// no-op closure.
func MigrateSpan(ctx context.Context, from, to int) (context.Context, func(applied int, err error)) {
	_ = from
	_ = to
	return ctx, func(applied int, err error) {
		_ = applied
		_ = err
	}
}

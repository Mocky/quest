package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// MigrateSpan opens `quest.db.migrate` at span-start with the
// quest.schema.from and quest.schema.to attributes so the backend can
// index on them even if the span is cut short by a migration error
// (OTEL.md §8.8). The returned end(applied, err) closure sets
// quest.schema.applied_count, applies the three-step error pattern on
// error, ends the span, and increments the
// dept.quest.schema.migrations counter exactly once. Callers (the
// dispatcher; quest init) gate on `from < to` before calling so the
// metric counts migrations-run, not checks-attempted.
func MigrateSpan(ctx context.Context, from, to int) (context.Context, func(applied int, err error)) {
	ctx, span := tracer.Start(ctx, "quest.db.migrate",
		trace.WithAttributes(
			attribute.Int("quest.schema.from", from),
			attribute.Int("quest.schema.to", to),
		),
	)
	return ctx, func(applied int, err error) {
		span.SetAttributes(attribute.Int("quest.schema.applied_count", applied))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, Truncate(err.Error(), 256))
		}
		span.End()
		if schemaMigrationsCtr != nil {
			schemaMigrationsCtr.Add(ctx, 1, metric.WithAttributes(
				attribute.Int("from_version", from),
				attribute.Int("to_version", to),
			))
		}
	}
}

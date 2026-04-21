package telemetry

import (
	"context"
	stderrors "errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// dbSystemAttr is cached at first access via sync.Once — every
// quest.store.tx span carries db.system="sqlite" and re-allocating an
// attribute.KeyValue per transaction adds up under DML-heavy load
// (OTEL.md §9.2 of the framework guide / §4.3 here).
var (
	dbSystemAttrOnce sync.Once
	dbSystemAttrKV   attribute.KeyValue
)

func dbSystemAttribute() attribute.KeyValue {
	dbSystemAttrOnce.Do(func() {
		dbSystemAttrKV = attribute.String("db.system", "sqlite")
	})
	return dbSystemAttrKV
}

// WrapStore returns the InstrumentedStore decorator when telemetry is
// enabled and the bare store otherwise. WrapStore is idempotent — if
// `s` is already an *InstrumentedStore, it is returned unchanged so a
// future double-wrap (the dispatcher and `quest init` both wrap the
// store) cannot emit duplicate quest.store.tx spans (OTEL.md §8.3).
func WrapStore(s store.Store) store.Store {
	if !enabled() {
		return s
	}
	if _, already := s.(*InstrumentedStore); already {
		return s
	}
	return &InstrumentedStore{inner: s}
}

// InstrumentedStore wraps any store.Store with quest.store.tx span
// emission around BeginImmediate. Read methods pass through unchanged
// — the named graph/move/list traversal spans are emitted by handlers
// via StoreSpan, not by the decorator.
type InstrumentedStore struct {
	inner store.Store
}

// Read pass-throughs.
func (d *InstrumentedStore) GetTask(ctx context.Context, id string) (store.Task, error) {
	return d.inner.GetTask(ctx, id)
}

func (d *InstrumentedStore) GetTaskWithDeps(ctx context.Context, id string) (store.Task, error) {
	return d.inner.GetTaskWithDeps(ctx, id)
}

func (d *InstrumentedStore) ListTasks(ctx context.Context, filter store.Filter) ([]store.Task, error) {
	return d.inner.ListTasks(ctx, filter)
}

func (d *InstrumentedStore) GetHistory(ctx context.Context, id string) ([]store.History, error) {
	return d.inner.GetHistory(ctx, id)
}

func (d *InstrumentedStore) GetChildren(ctx context.Context, parentID string) ([]store.Task, error) {
	return d.inner.GetChildren(ctx, parentID)
}

func (d *InstrumentedStore) GetDependencies(ctx context.Context, id string) ([]store.Dependency, error) {
	return d.inner.GetDependencies(ctx, id)
}

func (d *InstrumentedStore) GetDependents(ctx context.Context, id string) ([]store.Dependency, error) {
	return d.inner.GetDependents(ctx, id)
}

func (d *InstrumentedStore) GetTags(ctx context.Context, id string) ([]string, error) {
	return d.inner.GetTags(ctx, id)
}

func (d *InstrumentedStore) GetPRs(ctx context.Context, id string) ([]store.PR, error) {
	return d.inner.GetPRs(ctx, id)
}

func (d *InstrumentedStore) GetCommits(ctx context.Context, id string) ([]store.Commit, error) {
	return d.inner.GetCommits(ctx, id)
}

func (d *InstrumentedStore) GetNotes(ctx context.Context, id string) ([]store.Note, error) {
	return d.inner.GetNotes(ctx, id)
}

func (d *InstrumentedStore) Close() error { return d.inner.Close() }
func (d *InstrumentedStore) CurrentSchemaVersion(ctx context.Context) (int, error) {
	return d.inner.CurrentSchemaVersion(ctx)
}

// Snapshot is a pass-through. The snapshot primitive is cross-cutting
// (used by quest backup and by the dispatcher's pre-migration path)
// and its telemetry span is opened by the caller, not by this
// decorator — see the §4.4 snapshot-span follow-up in the backup plan.
func (d *InstrumentedStore) Snapshot(ctx context.Context, dstPath string) (int64, error) {
	return d.inner.Snapshot(ctx, dstPath)
}

// Unwrap exposes the inner store so store.Migrate can drill through
// the decorator and reach the *sqliteStore for its embedded.SQL
// migration runner. Migration runs from the bare store directly so
// the migration transaction does not also produce a quest.store.tx
// span — schema migrations get their own quest.db.migrate span via
// telemetry.MigrateSpan instead (OTEL.md §8.8).
func (d *InstrumentedStore) Unwrap() store.Store { return d.inner }

// BeginImmediate is the structural-transaction seam: opens the
// underlying *store.Tx, then wraps it in a quest.store.tx span whose
// lifetime is bound to the transaction's. Hooks installed via
// tx.SetHooks fire from Commit/Rollback to end the span with the
// outcome-classifying attributes.
func (d *InstrumentedStore) BeginImmediate(ctx context.Context, kind store.TxKind) (*store.Tx, error) {
	tx, err := d.inner.BeginImmediate(ctx, kind)
	if err != nil {
		return nil, err
	}
	_, span := tracer.Start(ctx, "quest.store.tx",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(tx.InvokedAt()),
		trace.WithAttributes(
			dbSystemAttribute(),
			attribute.String("quest.tx.kind", string(kind)),
		),
	)
	hookOnCommit := func(tx *store.Tx) {
		elapsed := time.Since(tx.InvokedAt())
		applyOutcome(span, tx)
		span.End()
		recordTxMetrics(ctx, tx, elapsed, false)
	}
	hookOnRollback := func(tx *store.Tx, hookErr error) {
		elapsed := time.Since(tx.InvokedAt())
		applyOutcome(span, tx)
		isLockTimeout := stderrors.Is(hookErr, errors.ErrTransient)
		if hookErr != nil {
			span.RecordError(hookErr)
			span.SetStatus(codes.Error, Truncate(hookErr.Error(), 256))
			span.SetAttributes(
				attribute.String("quest.error.class", errors.Class(hookErr)),
				attribute.Bool("quest.error.retryable", errors.Retryable(hookErr)),
				attribute.Int("quest.exit_code", errors.ExitCode(hookErr)),
			)
		}
		if isLockTimeout {
			setLockTimeoutAttrs(span, tx.LockWait())
		}
		span.End()
		recordTxMetrics(ctx, tx, elapsed, isLockTimeout)
	}
	tx.SetHooks(hookOnCommit, hookOnRollback)
	return tx, nil
}

// applyOutcome stamps quest.tx.lock_wait_ms, quest.tx.rows_affected,
// and quest.tx.outcome on the span. Outcome is read off the *store.Tx
// (the store package classifies based on Commit/Rollback flow + the
// optional MarkOutcome override).
func applyOutcome(span trace.Span, tx *store.Tx) {
	span.SetAttributes(
		attribute.Float64("quest.tx.lock_wait_ms", durationMS(tx.LockWait().Microseconds())),
		attribute.Int64("quest.tx.rows_affected", tx.RowsAffected()),
		attribute.String("quest.tx.outcome", string(tx.Outcome())),
	)
}

// recordTxMetrics increments the dept.quest.store.tx.duration and
// dept.quest.store.tx.lock_wait histograms (and dept.quest.store.lock_timeouts
// on exit-7). Tx duration is measured from InvokedAt (when the handler
// requested the lock) so the histogram includes lock-wait + body —
// subtracting the lock_wait histogram isolates body time.
func recordTxMetrics(ctx context.Context, tx *store.Tx, total time.Duration, lockTimeout bool) {
	kindAttr := metric.WithAttributes(attribute.String("tx_kind", string(tx.Kind())))
	if storeTxDurationHis != nil {
		storeTxDurationHis.Record(ctx, durationMS(total.Microseconds()), kindAttr)
	}
	if storeTxLockWaitHis != nil {
		storeTxLockWaitHis.Record(ctx, durationMS(tx.LockWait().Microseconds()), kindAttr)
	}
	if lockTimeout && storeLockTimeoutsCtr != nil {
		storeLockTimeoutsCtr.Add(ctx, 1, kindAttr)
	}
}

// durationMS converts microseconds to a sub-millisecond float so fast
// transactions don't truncate to 0. Microseconds is the smallest unit
// Go's time.Duration helpers expose; dividing keeps the unit "ms" for
// the OTEL histogram regardless of how short the transaction was.
func durationMS(micros int64) float64 {
	return float64(micros) / 1000.0
}

// setLockTimeoutAttrs stamps quest.lock.wait_limit_ms and
// quest.lock.wait_actual_ms on the rolled-back span when the inner
// Commit returned ErrTransient. wait_actual_ms is a Float64 routed
// through durationMS so a sub-millisecond commit-time SQLITE_BUSY
// (the LockWait captured at BeginImmediate) does not truncate to 0
// and break the daemon-upgrade retrospective signal in OTEL.md §15.
func setLockTimeoutAttrs(span trace.Span, lockWait time.Duration) {
	span.SetAttributes(
		attribute.Int("quest.lock.wait_limit_ms", 5000),
		attribute.Float64("quest.lock.wait_actual_ms", durationMS(lockWait.Microseconds())),
	)
}

// StoreSpan opens a child span under the active command span for
// store-level operations (`quest.store.traverse`,
// `quest.store.rename_subgraph`). Handlers call it when they need a
// named child span without importing go.opentelemetry.io/otel/trace.
// The returned end closure applies the three-step error pattern when
// err != nil and ends the span unconditionally.
func StoreSpan(ctx context.Context, name string) (context.Context, func(err error)) {
	ctx, span := tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindInternal))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, Truncate(err.Error(), 256))
		}
		span.End()
	}
}

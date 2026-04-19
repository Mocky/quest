package store

import (
	"context"
	"database/sql"
	stderrors "errors"
	"log/slog"
	"time"

	"github.com/mocky/quest/internal/errors"
)

// Tx wraps a BEGIN IMMEDIATE transaction returned by BeginImmediate.
// Handlers use the explicit methods only — the inner *sql.Tx is
// unexported. The invokedAt / startedAt pair records lock-wait
// duration; rowsAffected accumulates as handlers issue ExecContext so
// the decorator's onCommit hook (Task 12.4) can stamp the
// quest.tx.rows_affected span attribute without asking the handler to
// tally. outcome defaults to committed/rolled_back_error and is
// overridden by MarkOutcome or by a typed-error check in
// Commit/Rollback.
type Tx struct {
	inner        *sql.Tx
	kind         TxKind
	invokedAt    time.Time
	startedAt    time.Time
	rowsAffected int64
	outcome      TxOutcome
	// onCommit / onRollback are populated by the InstrumentedStore
	// decorator (Task 12.4). Nil on the bare store; the bookend slog
	// records fire regardless of whether the decorator is wrapped
	// around the store, per OBSERVABILITY.md §Per-Transaction
	// Boundaries.
	onCommit   func(tx *Tx)
	onRollback func(tx *Tx, err error)
}

// BeginImmediate opens a write transaction and tags it with kind. The
// DSN's `_txlock=immediate` parameter causes db.BeginTx(ctx, nil) to
// issue BEGIN IMMEDIATE instead of the default deferred BEGIN; there
// is no separate Exec("BEGIN IMMEDIATE"). If the write lock cannot be
// acquired within PRAGMA busy_timeout (5000 ms), the driver returns
// SQLITE_BUSY which classifyDriverErr maps to ErrTransient (exit 7).
// Exit-7 is the only retryable class; handlers never retry internally
// — returning exit 7 lets the caller decide.
func (s *sqliteStore) BeginImmediate(ctx context.Context, kind TxKind) (*Tx, error) {
	invokedAt := time.Now()
	inner, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if stderrors.Is(err, context.Canceled) || stderrors.Is(err, context.DeadlineExceeded) {
			slog.WarnContext(ctx, "write lock timeout",
				"tx_kind", string(kind),
				"lock_wait_ms", durationMillis(time.Since(invokedAt)),
			)
			return nil, classifyDriverErr(err)
		}
		mapped := classifyDriverErr(err)
		if stderrors.Is(mapped, errors.ErrTransient) {
			slog.WarnContext(ctx, "write lock timeout",
				"tx_kind", string(kind),
				"lock_wait_ms", durationMillis(time.Since(invokedAt)),
			)
		}
		return nil, mapped
	}
	startedAt := time.Now()
	tx := &Tx{
		inner:     inner,
		kind:      kind,
		invokedAt: invokedAt,
		startedAt: startedAt,
	}
	slog.DebugContext(ctx, "BEGIN IMMEDIATE acquired",
		"tx_kind", string(kind),
		"lock_wait_ms", durationMillis(startedAt.Sub(invokedAt)),
	)
	return tx, nil
}

// Commit closes the transaction, fires the decorator hook (if any),
// and emits the committed-boundary slog record. When the caller has
// not explicitly marked an outcome, successful commit defaults to
// TxCommitted.
func (tx *Tx) Commit() error {
	err := tx.inner.Commit()
	if err == nil {
		if tx.outcome == "" {
			tx.outcome = TxCommitted
		}
		slog.Debug("tx committed",
			"tx_kind", string(tx.kind),
			"rows_affected", tx.rowsAffected,
		)
		if tx.onCommit != nil {
			tx.onCommit(tx)
		}
		return nil
	}
	mapped := classifyDriverErr(err)
	if tx.outcome == "" {
		tx.outcome = classifyOutcome(mapped)
	}
	slog.Debug("tx rolled back",
		"tx_kind", string(tx.kind),
		"outcome", string(tx.outcome),
	)
	if tx.onRollback != nil {
		tx.onRollback(tx, mapped)
	}
	return mapped
}

// Rollback closes the transaction without committing. Safe to call in
// a defer after Commit — the driver returns ErrTxDone which is
// swallowed here so handlers can defer Rollback() unconditionally.
func (tx *Tx) Rollback() error {
	err := tx.inner.Rollback()
	if stderrors.Is(err, sql.ErrTxDone) {
		return nil
	}
	if tx.outcome == "" {
		tx.outcome = TxRolledBackError
	}
	slog.Debug("tx rolled back",
		"tx_kind", string(tx.kind),
		"outcome", string(tx.outcome),
	)
	if tx.onRollback != nil {
		tx.onRollback(tx, err)
	}
	if err == nil {
		return nil
	}
	return classifyDriverErr(err)
}

// ExecContext is a thin wrapper that auto-accumulates rows_affected
// into the transaction's running sum. Handlers never need to call an
// explicit AddRowsAffected helper.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := tx.inner.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if res != nil {
		if n, rerr := res.RowsAffected(); rerr == nil && n >= 0 {
			tx.rowsAffected += n
		}
	}
	return res, nil
}

// QueryContext / QueryRowContext are pass-throughs — the underlying
// *sql.Tx already carries a context and supports cancellation.
func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.inner.QueryContext(ctx, query, args...)
}

func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.inner.QueryRowContext(ctx, query, args...)
}

// MarkOutcome lets handlers override the default outcome classification
// for dashboards — rolled_back_precondition (expected check failed,
// exit 5) vs rolled_back_error (unexpected). When unset, Commit /
// Rollback auto-infer from the returned error.
func (tx *Tx) MarkOutcome(outcome TxOutcome) {
	tx.outcome = outcome
}

// LockWait reports the delta between BeginImmediate being called and
// the underlying BEGIN IMMEDIATE returning. The decorator reads this
// to stamp quest.tx.lock_wait_ms on the store-tx span.
func (tx *Tx) LockWait() time.Duration {
	return tx.startedAt.Sub(tx.invokedAt)
}

// Kind, RowsAffected, and Outcome expose the fields the Task 12.4
// decorator needs to populate the quest.store.tx span attributes
// without reaching into package internals.
func (tx *Tx) Kind() TxKind        { return tx.kind }
func (tx *Tx) RowsAffected() int64 { return tx.rowsAffected }
func (tx *Tx) Outcome() TxOutcome  { return tx.outcome }

// InvokedAt returns the timestamp captured before db.BeginTx so the
// InstrumentedStore decorator can stamp the quest.store.tx span with
// the lock-wait period via trace.WithTimestamp(tx.InvokedAt()).
func (tx *Tx) InvokedAt() time.Time { return tx.invokedAt }

// SetHooks installs the decorator's onCommit / onRollback closures.
// Either may be nil. Called once by the InstrumentedStore decorator
// immediately after BeginImmediate returns; the bare store leaves
// both fields nil and the Commit/Rollback methods skip the call when
// either is unset.
func (tx *Tx) SetHooks(onCommit func(tx *Tx), onRollback func(tx *Tx, err error)) {
	tx.onCommit = onCommit
	tx.onRollback = onRollback
}

func classifyOutcome(err error) TxOutcome {
	switch {
	case err == nil:
		return TxCommitted
	case stderrors.Is(err, errors.ErrConflict),
		stderrors.Is(err, errors.ErrNotFound),
		stderrors.Is(err, errors.ErrPermission):
		return TxRolledBackPrecondition
	default:
		return TxRolledBackError
	}
}

func durationMillis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

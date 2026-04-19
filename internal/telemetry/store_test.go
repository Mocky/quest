package telemetry

import (
	"context"
	"database/sql"
	stderrors "errors"
	"path/filepath"
	"testing"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// TestWrapStoreIdempotent confirms a double-wrap is a no-op so the
// dispatcher and quest init can both call WrapStore without producing
// duplicate quest.store.tx spans (OTEL.md §8.3).
func TestWrapStoreIdempotent(t *testing.T) {
	prevEnabled := enabled()
	markEnabled()
	defer func() {
		if !prevEnabled {
			markDisabled()
		}
	}()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quest.db")
	bare, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer bare.Close()

	wrapped1 := WrapStore(bare)
	if _, ok := wrapped1.(*InstrumentedStore); !ok {
		t.Fatalf("first WrapStore did not return *InstrumentedStore: %T", wrapped1)
	}
	wrapped2 := WrapStore(wrapped1)
	if wrapped1 != wrapped2 {
		t.Errorf("second WrapStore wrapped twice; want same instance back")
	}
}

// TestWrapStoreDisabledPassthrough confirms the disabled path returns
// the bare store unchanged so call sites pay no decorator overhead
// when telemetry is off.
func TestWrapStoreDisabledPassthrough(t *testing.T) {
	prevEnabled := enabled()
	markDisabled()
	defer func() {
		if prevEnabled {
			markEnabled()
		}
	}()

	dir := t.TempDir()
	bare, err := store.Open(filepath.Join(dir, "q.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer bare.Close()
	got := WrapStore(bare)
	if got != bare {
		t.Errorf("disabled WrapStore returned %T; want bare store back", got)
	}
}

// TestInstrumentedStoreEmitsTxSpan opens a real SQLite DB, runs a
// migration, performs a structural transaction through the decorator,
// and asserts the exporter captures exactly one quest.store.tx span
// with the expected attribute set. Layer 3 — uses the real store
// because mocking *sql.Tx is brittle.
func TestInstrumentedStoreEmitsTxSpan(t *testing.T) {
	exp := installInMemoryTracer(t)
	prevEnabled := enabled()
	markEnabled()
	defer func() {
		if !prevEnabled {
			markDisabled()
		}
	}()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quest.db")
	bare, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer bare.Close()
	if _, err := store.Migrate(context.Background(), bare); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	wrapped := WrapStore(bare)
	tx, err := wrapped.BeginImmediate(context.Background(), store.TxAccept)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks (id, title, type, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		"test-1", "test", "task", "open", "2026-04-19T00:00:00Z",
	); err != nil {
		_ = tx.Rollback()
		t.Fatalf("ExecContext: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want 1 quest.store.tx span", len(spans))
	}
	s := spans[0]
	if s.Name != "quest.store.tx" {
		t.Errorf("span name = %q; want quest.store.tx", s.Name)
	}
	wantAttrs := map[string]any{
		"db.system":              "sqlite",
		"quest.tx.kind":          "accept",
		"quest.tx.outcome":       "committed",
		"quest.tx.rows_affected": int64(1),
	}
	got := map[string]any{}
	for _, kv := range s.Attributes {
		switch kv.Key {
		case "db.system", "quest.tx.kind", "quest.tx.outcome":
			got[string(kv.Key)] = kv.Value.AsString()
		case "quest.tx.rows_affected":
			got[string(kv.Key)] = kv.Value.AsInt64()
		}
	}
	for k, want := range wantAttrs {
		if got[k] != want {
			t.Errorf("attr %q = %v; want %v", k, got[k], want)
		}
	}
	// lock_wait_ms must be present and non-negative.
	foundLW := false
	for _, kv := range s.Attributes {
		if kv.Key == "quest.tx.lock_wait_ms" {
			foundLW = true
			if kv.Value.AsFloat64() < 0 {
				t.Errorf("lock_wait_ms = %v; want >= 0", kv.Value.AsFloat64())
			}
		}
	}
	if !foundLW {
		t.Errorf("quest.tx.lock_wait_ms attribute missing")
	}
}

// TestInstrumentedStorePreconditionRollback walks the
// rolled_back_precondition outcome path: handler MarkOutcome before
// Rollback. Confirms the span attribute carries the right tag for
// dashboard distinction between expected vs unexpected rollback.
func TestInstrumentedStorePreconditionRollback(t *testing.T) {
	exp := installInMemoryTracer(t)
	prevEnabled := enabled()
	markEnabled()
	defer func() {
		if !prevEnabled {
			markDisabled()
		}
	}()

	dir := t.TempDir()
	bare, err := store.Open(filepath.Join(dir, "q.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer bare.Close()
	if _, err := store.Migrate(context.Background(), bare); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	wrapped := WrapStore(bare)
	tx, err := wrapped.BeginImmediate(context.Background(), store.TxAccept)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	tx.MarkOutcome(store.TxRolledBackPrecondition)
	if err := tx.Rollback(); err != nil && !stderrors.Is(err, sql.ErrTxDone) {
		t.Fatalf("Rollback: %v", err)
	}

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want 1", len(spans))
	}
	for _, kv := range spans[0].Attributes {
		if kv.Key == "quest.tx.outcome" && kv.Value.AsString() != string(store.TxRolledBackPrecondition) {
			t.Errorf("outcome = %s; want %s", kv.Value.AsString(), store.TxRolledBackPrecondition)
		}
	}
}

// TestStoreSpanEndsWithError confirms StoreSpan applies the three-step
// error pattern: span gets RecordError + SetStatus(Error, ...) + the
// span is ended either way.
func TestStoreSpanEndsWithError(t *testing.T) {
	exp := installInMemoryTracer(t)
	_, end := StoreSpan(context.Background(), "quest.store.traverse")
	end(stderrors.New("boom"))

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want 1", len(spans))
	}
	if spans[0].Status.Code.String() != "Error" {
		t.Errorf("status = %s; want Error", spans[0].Status.Code)
	}
}

// TestStoreSpanSuccess confirms StoreSpan ends without error and the
// span has Unset status when the closure receives a nil error.
func TestStoreSpanSuccess(t *testing.T) {
	exp := installInMemoryTracer(t)
	_, end := StoreSpan(context.Background(), "quest.store.traverse")
	end(nil)
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want 1", len(spans))
	}
	if spans[0].Status.Code.String() != "Unset" {
		t.Errorf("status = %s; want Unset", spans[0].Status.Code)
	}
}

// Make errors import effective even when conditions branch around it.
var _ = errors.ErrTransient

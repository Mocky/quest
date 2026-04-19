package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/mocky/quest/internal/errors"
)

// installCapturingMeter swaps the global meter for one backed by a
// manual reader and re-runs initSchemaMigrationsInstrument so the
// package-level handles point at the captured provider. Cleanup
// restores the previous meter and re-registers nil instruments so
// subsequent tests start fresh.
func installCapturingMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	prevMP := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	prevMeter := meter
	meter = mp.Meter("dept.quest")
	initSchemaMigrationsInstrument()
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prevMP)
		meter = prevMeter
		// Reset all instruments so the next test installs fresh ones.
		schemaMigrationsCtr = nil
		operationsCtr = nil
		errorsCtr = nil
		tasksCreatedCtr = nil
		tasksCompletedCtr = nil
		statusTransitionsCtr = nil
		linksCtr = nil
		batchSizeHis = nil
		batchErrorsCtr = nil
		storeTxDurationHis = nil
		storeTxLockWaitHis = nil
		storeLockTimeoutsCtr = nil
		queryResultCountHis = nil
		graphTraversalHis = nil
		operationDurationHis = nil
	})
	return reader
}

// collect drains the reader into a ResourceMetrics value.
func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rm
}

// TestInstrumentInventory pins every instrument from §5.1 — a
// regression that loses one fails this test (Task 12.5 contract).
func TestInstrumentInventory(t *testing.T) {
	installCapturingMeter(t)
	want := []string{
		"dept.quest.operations",
		"dept.quest.operation.duration",
		"dept.quest.errors",
		"dept.quest.tasks.created",
		"dept.quest.tasks.completed",
		"dept.quest.status_transitions",
		"dept.quest.links",
		"dept.quest.batch.size",
		"dept.quest.batch.errors",
		"dept.quest.store.tx.duration",
		"dept.quest.store.tx.lock_wait",
		"dept.quest.store.lock_timeouts",
		"dept.quest.query.result_count",
		"dept.quest.graph.traversal_nodes",
		"dept.quest.schema.migrations",
	}
	// All instruments must be non-nil.
	insts := map[string]any{
		"dept.quest.operations":            operationsCtr,
		"dept.quest.operation.duration":    operationDurationHis,
		"dept.quest.errors":                errorsCtr,
		"dept.quest.tasks.created":         tasksCreatedCtr,
		"dept.quest.tasks.completed":       tasksCompletedCtr,
		"dept.quest.status_transitions":    statusTransitionsCtr,
		"dept.quest.links":                 linksCtr,
		"dept.quest.batch.size":            batchSizeHis,
		"dept.quest.batch.errors":          batchErrorsCtr,
		"dept.quest.store.tx.duration":     storeTxDurationHis,
		"dept.quest.store.tx.lock_wait":    storeTxLockWaitHis,
		"dept.quest.store.lock_timeouts":   storeLockTimeoutsCtr,
		"dept.quest.query.result_count":    queryResultCountHis,
		"dept.quest.graph.traversal_nodes": graphTraversalHis,
		"dept.quest.schema.migrations":     schemaMigrationsCtr,
	}
	for _, name := range want {
		if insts[name] == nil {
			t.Errorf("instrument %q not registered", name)
		}
	}
}

// TestRecordHandlerErrorAttributes verifies the §C1 attribute set
// (quest.error.class, quest.error.retryable, quest.exit_code) lands on
// the active span and that dept.quest.errors{error_class} increments
// once per call.
func TestRecordHandlerErrorAttributes(t *testing.T) {
	exp := installInMemoryTracer(t)
	reader := installCapturingMeter(t)

	ctx, span := CommandSpan(context.Background(), "accept", false)
	wantErr := fmt.Errorf("%w: parent not in open", errors.ErrConflict)
	RecordHandlerError(ctx, wantErr)
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want 1", len(spans))
	}
	gotClass, gotRetryable, gotExit := "", false, int64(0)
	for _, kv := range spans[0].Attributes {
		switch kv.Key {
		case "quest.error.class":
			gotClass = kv.Value.AsString()
		case "quest.error.retryable":
			gotRetryable = kv.Value.AsBool()
		case "quest.exit_code":
			gotExit = kv.Value.AsInt64()
		}
	}
	if gotClass != "conflict" || gotRetryable != false || gotExit != 5 {
		t.Errorf("attrs class=%q retryable=%v exit=%d; want conflict/false/5", gotClass, gotRetryable, gotExit)
	}

	rm := collect(t, reader)
	gotCount := int64(0)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "dept.quest.errors" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				for _, kv := range dp.Attributes.ToSlice() {
					if kv.Key == "error_class" && kv.Value.AsString() == "conflict" {
						gotCount += dp.Value
					}
				}
			}
		}
	}
	if gotCount != 1 {
		t.Errorf("dept.quest.errors{error_class=conflict} = %d; want 1", gotCount)
	}
}

// TestRecordHandlerErrorRetryable confirms exit-7 sets retryable=true.
func TestRecordHandlerErrorRetryable(t *testing.T) {
	exp := installInMemoryTracer(t)
	installCapturingMeter(t)
	ctx, span := CommandSpan(context.Background(), "accept", false)
	RecordHandlerError(ctx, fmt.Errorf("%w: lock", errors.ErrTransient))
	span.End()
	for _, kv := range exp.GetSpans()[0].Attributes {
		if kv.Key == "quest.error.retryable" && !kv.Value.AsBool() {
			t.Errorf("retryable should be true for ErrTransient")
		}
	}
}

// TestRecordDispatchErrorEmitsStderrAndOps verifies the dispatcher
// helper writes the canonical two-liner, increments operations{error},
// and returns the mapped exit code.
func TestRecordDispatchErrorEmitsStderrAndOps(t *testing.T) {
	installInMemoryTracer(t)
	reader := installCapturingMeter(t)

	var stderr bytes.Buffer
	code := RecordDispatchError(context.Background(), errors.ErrUsage, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d; want 2", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("quest: usage_error: ")) {
		t.Errorf("stderr missing canonical line: %q", stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("quest: exit 2 (usage_error)")) {
		t.Errorf("stderr missing exit line: %q", stderr.String())
	}

	rm := collect(t, reader)
	gotErrorOps := int64(0)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "dept.quest.operations" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				for _, kv := range dp.Attributes.ToSlice() {
					if kv.Key == "status" && kv.Value.AsString() == "error" {
						gotErrorOps += dp.Value
					}
				}
			}
		}
	}
	if gotErrorOps != 1 {
		t.Errorf("operations{status=error} = %d; want 1", gotErrorOps)
	}
}

// TestRecordDispatchErrorNilNoOp confirms passing nil produces exit 0
// without touching counters or stderr.
func TestRecordDispatchErrorNilNoOp(t *testing.T) {
	installInMemoryTracer(t)
	installCapturingMeter(t)
	var stderr bytes.Buffer
	if code := RecordDispatchError(context.Background(), nil, &stderr); code != 0 {
		t.Errorf("nil err code = %d; want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr non-empty for nil err: %q", stderr.String())
	}
}

// TestExitCodeClassMapping iterates every exit code 1-7 and asserts
// the class string emitted on the span matches §4.4.
func TestExitCodeClassMapping(t *testing.T) {
	cases := []struct {
		err   error
		class string
		exit  int
	}{
		{errors.ErrGeneral, "general_failure", 1},
		{errors.ErrUsage, "usage_error", 2},
		{errors.ErrNotFound, "not_found", 3},
		{errors.ErrPermission, "permission_denied", 4},
		{errors.ErrConflict, "conflict", 5},
		{errors.ErrRoleDenied, "role_denied", 6},
		{errors.ErrTransient, "transient_failure", 7},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			exp := installInMemoryTracer(t)
			installCapturingMeter(t)
			ctx, span := CommandSpan(context.Background(), "any", false)
			RecordHandlerError(ctx, fmt.Errorf("%w: x", tc.err))
			span.End()

			gotClass, gotExit := "", int64(0)
			for _, kv := range exp.GetSpans()[0].Attributes {
				if kv.Key == "quest.error.class" {
					gotClass = kv.Value.AsString()
				}
				if kv.Key == "quest.exit_code" {
					gotExit = kv.Value.AsInt64()
				}
			}
			if gotClass != tc.class {
				t.Errorf("class = %q; want %q", gotClass, tc.class)
			}
			if gotExit != int64(tc.exit) {
				t.Errorf("exit = %d; want %d", gotExit, tc.exit)
			}
		})
	}
}

// silence unused vars in case of build-flag drift.
var _ = sync.Once{}

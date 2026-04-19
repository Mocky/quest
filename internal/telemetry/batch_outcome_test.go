package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestBatchOutcomeClassificationAndAttributes covers the §4.3 batch
// attribute set + the §5.1 dept.quest.batch.size histogram with the
// outcome dimension. Tests every classification branch.
func TestBatchOutcomeClassificationAndAttributes(t *testing.T) {
	cases := []struct {
		name        string
		linesTotal  int
		linesBlank  int
		partialOK   bool
		created     int
		errors      int
		wantOutcome string
	}{
		{"all_ok", 3, 0, false, 3, 0, "ok"},
		{"all_rejected_atomic", 3, 0, false, 0, 3, "rejected"},
		{"partial_ok", 3, 1, true, 2, 1, "partial"},
		{"all_rejected_partial_too", 3, 0, true, 0, 3, "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := installInMemoryTracer(t)
			reader := installCapturingMeter(t)

			ctx, span := CommandSpan(context.Background(), "batch", true)
			RecordBatchOutcome(ctx, tc.linesTotal, tc.linesBlank, tc.partialOK, tc.created, tc.errors)
			span.End()

			got := map[string]any{}
			for _, kv := range exp.GetSpans()[0].Attributes {
				got[string(kv.Key)] = kv.Value.AsInterface()
			}
			if got["quest.batch.outcome"] != tc.wantOutcome {
				t.Errorf("outcome = %v; want %s", got["quest.batch.outcome"], tc.wantOutcome)
			}
			if got["quest.batch.lines_total"] != int64(tc.linesTotal) {
				t.Errorf("lines_total = %v; want %d", got["quest.batch.lines_total"], tc.linesTotal)
			}
			if got["quest.batch.lines_blank"] != int64(tc.linesBlank) {
				t.Errorf("lines_blank = %v; want %d", got["quest.batch.lines_blank"], tc.linesBlank)
			}
			if got["quest.batch.partial_ok"] != tc.partialOK {
				t.Errorf("partial_ok = %v; want %v", got["quest.batch.partial_ok"], tc.partialOK)
			}
			if got["quest.batch.created"] != int64(tc.created) {
				t.Errorf("created = %v; want %d", got["quest.batch.created"], tc.created)
			}
			if got["quest.batch.errors"] != int64(tc.errors) {
				t.Errorf("errors = %v; want %d", got["quest.batch.errors"], tc.errors)
			}

			rm := collect(t, reader)
			gotOutcome := ""
			gotCreated := int64(-1)
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name != "dept.quest.batch.size" {
						continue
					}
					hist, ok := m.Data.(metricdata.Histogram[int64])
					if !ok {
						continue
					}
					for _, dp := range hist.DataPoints {
						for _, kv := range dp.Attributes.ToSlice() {
							if kv.Key == "outcome" {
								gotOutcome = kv.Value.AsString()
							}
						}
						gotCreated = dp.Sum
					}
				}
			}
			if gotOutcome != tc.wantOutcome {
				t.Errorf("hist outcome = %s; want %s", gotOutcome, tc.wantOutcome)
			}
			if gotCreated != int64(tc.created) {
				t.Errorf("hist value = %d; want %d", gotCreated, tc.created)
			}
		})
	}
}

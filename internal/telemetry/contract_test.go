package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestRoleUnsetRendering pins the §8.6 single-source rendering rule:
// when AGENT_ROLE is empty, both the span attribute (gen_ai.agent.name)
// and the metric dimension (role) carry the literal "unset". A
// regression that introduces "" or "none" in either signal would split
// dashboards across two role values for the same invocation.
//
// Unit-level — assert roleOrUnset directly plus its application path
// through CommandSpan and RecordTaskCreated. Cross-package handler
// round-trip lives in internal/cli/contract_test.go (TestChildSpansOmitGenAIAttributes
// covers the related child-span carve-out).
func TestRoleUnsetRendering(t *testing.T) {
	t.Run("HelperReturnsUnset", func(t *testing.T) {
		if got := roleOrUnset(""); got != "unset" {
			t.Errorf("roleOrUnset(\"\") = %q; want %q", got, "unset")
		}
		if got := roleOrUnset("planner"); got != "planner" {
			t.Errorf("roleOrUnset(\"planner\") = %q; want %q", got, "planner")
		}
	})

	t.Run("CommandSpanCarriesUnset", func(t *testing.T) {
		exp := installInMemoryTracer(t)
		defer setIdentity("", "", "")
		setIdentity("", "task-1", "sess-1")

		_, span := CommandSpan(context.Background(), "show", false)
		span.End()

		spans := exp.GetSpans()
		if len(spans) != 1 {
			t.Fatalf("got %d spans; want 1", len(spans))
		}
		got := ""
		for _, kv := range spans[0].Attributes {
			if kv.Key == "gen_ai.agent.name" {
				got = kv.Value.AsString()
			}
		}
		if got != "unset" {
			t.Errorf("gen_ai.agent.name = %q; want \"unset\"", got)
		}
	})

	t.Run("MetricDimensionCarriesUnset", func(t *testing.T) {
		reader := installCapturingMeter(t)
		// Record a tasks.created event with empty role — recorder runs
		// the value through roleOrUnset before stamping the dimension.
		RecordTaskCreated(context.Background(), "proj-a1", "T2", "", "task")

		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("Collect: %v", err)
		}
		got := ""
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != "dept.quest.tasks.created" {
					continue
				}
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					continue
				}
				for _, dp := range sum.DataPoints {
					for _, kv := range dp.Attributes.ToSlice() {
						if kv.Key == "role" {
							got = kv.Value.AsString()
						}
					}
				}
			}
		}
		if got != "unset" {
			t.Errorf("dept.quest.tasks.created{role} = %q; want \"unset\"", got)
		}
	})
}

// _ keeps the manual-reader / attribute imports live even when the
// happy path elides them.
var (
	_ = sdkmetric.NewManualReader
	_ = attribute.String
)

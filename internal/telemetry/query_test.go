package telemetry

import (
	"context"
	"testing"
)

// TestRecordQueryResultAttributes confirms the §4.3 query attribute
// set: bounded-enum filters land as comma-joined strings, ready as a
// bool, result_count as an int. Tag and parent filters are not
// emitted regardless.
func TestRecordQueryResultAttributes(t *testing.T) {
	exp := installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "list", true)
	RecordQueryResult(ctx, "list", 7, QueryFilter{
		Status: []string{"open", "accepted"},
		Tier:   []string{"T2"},
		Ready:  true,
	})
	span.End()

	got := map[string]any{}
	for _, kv := range exp.GetSpans()[0].Attributes {
		got[string(kv.Key)] = kv.Value.AsInterface()
	}
	if got["quest.query.result_count"] != int64(7) {
		t.Errorf("result_count = %v; want 7", got["quest.query.result_count"])
	}
	if got["quest.query.filter.status"] != "accepted,open" {
		t.Errorf("status filter = %v; want sorted accepted,open", got["quest.query.filter.status"])
	}
	if got["quest.query.filter.tier"] != "T2" {
		t.Errorf("tier filter = %v; want T2", got["quest.query.filter.tier"])
	}
	if got["quest.query.ready"] != true {
		t.Errorf("ready = %v; want true", got["quest.query.ready"])
	}
	if _, ok := got["quest.query.filter.role"]; ok {
		t.Errorf("role filter present even though unset")
	}
}

// TestRecordQueryResultExcludesUnboundedFilters confirms the OTEL.md
// §4.3 prohibition: tag and parent filters never appear as span
// attributes — they are unbounded cardinality.
func TestRecordQueryResultExcludesUnboundedFilters(t *testing.T) {
	exp := installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "list", true)
	// QueryFilter has no Tag or Parent fields by design — passing the
	// recorder a populated QueryFilter is the right pattern. Just call
	// once with bounded enums and assert the unbounded keys never appear.
	RecordQueryResult(ctx, "list", 1, QueryFilter{Status: []string{"open"}})
	span.End()
	for _, kv := range exp.GetSpans()[0].Attributes {
		if string(kv.Key) == "quest.query.filter.tag" || string(kv.Key) == "quest.query.filter.parent" {
			t.Errorf("forbidden attribute present: %s", kv.Key)
		}
	}
}

// TestRecordGraphResultAttributes confirms the §4.3 graph attribute
// set: quest.task.id (the rootID), node/edge/external counts on the
// span; quest.graph.traversal_nodes lives on the metric only.
func TestRecordGraphResultAttributes(t *testing.T) {
	exp := installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "graph", true)
	RecordGraphResult(ctx, "proj-a1", 12, 6, 2, 25)
	span.End()

	got := map[string]any{}
	for _, kv := range exp.GetSpans()[0].Attributes {
		got[string(kv.Key)] = kv.Value.AsInterface()
	}
	if got["quest.task.id"] != "proj-a1" {
		t.Errorf("quest.task.id = %v; want proj-a1", got["quest.task.id"])
	}
	if got["quest.graph.node_count"] != int64(12) {
		t.Errorf("node_count = %v; want 12", got["quest.graph.node_count"])
	}
	if got["quest.graph.edge_count"] != int64(6) {
		t.Errorf("edge_count = %v; want 6", got["quest.graph.edge_count"])
	}
	if got["quest.graph.external_count"] != int64(2) {
		t.Errorf("external_count = %v; want 2", got["quest.graph.external_count"])
	}
	if _, ok := got["quest.graph.traversal_nodes"]; ok {
		t.Errorf("quest.graph.traversal_nodes present as span attr; should be metric-only")
	}
	if _, ok := got["quest.graph.root_id"]; ok {
		t.Errorf("quest.graph.root_id present; should use quest.task.id")
	}
}

package telemetry

import (
	"context"
	"testing"
)

// TestRecordMoveOutcomeAttributes confirms the §4.3 move attribute set
// on the active span: old_id, new_id, subgraph_size, dep_updates.
func TestRecordMoveOutcomeAttributes(t *testing.T) {
	exp := installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "move", true)
	RecordMoveOutcome(ctx, "proj-a1.3", "proj-b2.1", 4, 5)
	span.End()

	got := map[string]any{}
	for _, kv := range exp.GetSpans()[0].Attributes {
		got[string(kv.Key)] = kv.Value.AsInterface()
	}
	if got["quest.move.old_id"] != "proj-a1.3" {
		t.Errorf("old_id = %v; want proj-a1.3", got["quest.move.old_id"])
	}
	if got["quest.move.new_id"] != "proj-b2.1" {
		t.Errorf("new_id = %v; want proj-b2.1", got["quest.move.new_id"])
	}
	if got["quest.move.subgraph_size"] != int64(4) {
		t.Errorf("subgraph_size = %v; want 4", got["quest.move.subgraph_size"])
	}
	if got["quest.move.dep_updates"] != int64(5) {
		t.Errorf("dep_updates = %v; want 5", got["quest.move.dep_updates"])
	}
}

// TestRecordCancelOutcomeAttributes confirms the §4.3 cancel attribute
// set: quest.task.id (the canonical task-affecting row), recursive,
// cancelled_count, skipped_count. No proprietary
// quest.cancel.target_id duplicates the task ID.
func TestRecordCancelOutcomeAttributes(t *testing.T) {
	exp := installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "cancel", true)
	RecordCancelOutcome(ctx, "proj-a1", true, 3, 1)
	span.End()

	got := map[string]any{}
	for _, kv := range exp.GetSpans()[0].Attributes {
		got[string(kv.Key)] = kv.Value.AsInterface()
	}
	if got["quest.task.id"] != "proj-a1" {
		t.Errorf("quest.task.id = %v; want proj-a1", got["quest.task.id"])
	}
	if got["quest.cancel.recursive"] != true {
		t.Errorf("recursive = %v; want true", got["quest.cancel.recursive"])
	}
	if got["quest.cancel.cancelled_count"] != int64(3) {
		t.Errorf("cancelled_count = %v; want 3", got["quest.cancel.cancelled_count"])
	}
	if got["quest.cancel.skipped_count"] != int64(1) {
		t.Errorf("skipped_count = %v; want 1", got["quest.cancel.skipped_count"])
	}
	if _, ok := got["quest.cancel.target_id"]; ok {
		t.Errorf("forbidden quest.cancel.target_id present; quest.task.id is the canonical row")
	}
}

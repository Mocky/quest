package telemetry

import (
	"context"
	"testing"
)

// TestValidateAndPhaseSpansShape confirms the batch validation tree
// matches OTEL.md §4.1: quest.validate parent with one named child per
// phase. Phase span emits the quest.batch.error event when
// RecordBatchError fires inside its scope.
func TestValidateAndPhaseSpansShape(t *testing.T) {
	exp := installInMemoryTracer(t)
	installCapturingMeter(t)

	ctx, cmdSpan := CommandSpan(context.Background(), "batch", true)
	vctx, vEnd := ValidateSpan(ctx)
	pctx, pEnd := BatchPhaseSpan(vctx, "parse")
	RecordBatchError(pctx, "parse", "missing_field", "title", "", 5)
	pEnd()
	rctx, rEnd := BatchPhaseSpan(vctx, "reference")
	RecordBatchError(rctx, "reference", "duplicate_ref", "ref", "ref-1", 7)
	rEnd()
	gctx, gEnd := BatchPhaseSpan(vctx, "graph")
	RecordCycleDetected(gctx, []string{"a", "b", "a"})
	gEnd()
	sctx, sEnd := BatchPhaseSpan(vctx, "semantic")
	_ = sctx
	sEnd()
	vEnd()
	cmdSpan.End()

	spans := exp.GetSpans()
	wantNames := []string{
		"quest.batch.parse",
		"quest.batch.reference",
		"quest.batch.graph",
		"quest.batch.semantic",
		"quest.validate",
		"execute_tool quest.batch",
	}
	gotNames := []string{}
	for _, s := range spans {
		gotNames = append(gotNames, s.Name)
	}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("got %d spans (%v); want %d (%v)", len(gotNames), gotNames, len(wantNames), wantNames)
	}
	for _, want := range wantNames {
		found := false
		for _, got := range gotNames {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing span %q", want)
		}
	}

	// quest.batch.parse must carry the quest.batch.error event for
	// missing_field/title/line=5.
	for _, s := range spans {
		if s.Name != "quest.batch.parse" {
			continue
		}
		if len(s.Events) != 1 {
			t.Errorf("parse span events = %d; want 1", len(s.Events))
			continue
		}
		ev := s.Events[0]
		if ev.Name != "quest.batch.error" {
			t.Errorf("parse event name = %q; want quest.batch.error", ev.Name)
		}
		gotCode := ""
		gotField := ""
		gotLine := int64(0)
		for _, kv := range ev.Attributes {
			switch kv.Key {
			case "code":
				gotCode = kv.Value.AsString()
			case "field":
				gotField = kv.Value.AsString()
			case "line":
				gotLine = kv.Value.AsInt64()
			}
		}
		if gotCode != "missing_field" || gotField != "title" || gotLine != 5 {
			t.Errorf("parse event attrs = code=%s field=%s line=%d; want missing_field/title/5",
				gotCode, gotField, gotLine)
		}
	}

	// quest.batch.graph must carry the quest.dep.cycle_detected event.
	for _, s := range spans {
		if s.Name != "quest.batch.graph" {
			continue
		}
		foundCycle := false
		for _, ev := range s.Events {
			if ev.Name == "quest.dep.cycle_detected" {
				foundCycle = true
				for _, kv := range ev.Attributes {
					if kv.Key == "quest.cycle.length" && kv.Value.AsInt64() != 3 {
						t.Errorf("cycle.length = %d; want 3", kv.Value.AsInt64())
					}
				}
			}
		}
		if !foundCycle {
			t.Errorf("graph span missing quest.dep.cycle_detected event")
		}
	}
}

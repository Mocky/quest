package telemetry

import (
	"context"
	"testing"
)

// TestGateSpanAllowedAndDenied confirms a quest.role.gate child span is
// emitted whether the gate allowed or denied the command — retrospective
// queries care about attempts, not just denials.
func TestGateSpanAllowedAndDenied(t *testing.T) {
	exp := installInMemoryTracer(t)

	ctx, parent := CommandSpan(context.Background(), "create", true)
	GateSpan(ctx, "planner", true)
	GateSpan(ctx, "", false)
	parent.End()

	spans := exp.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("got %d spans; want 3 (1 command + 2 gate)", len(spans))
	}
	gates := []map[string]any{}
	for _, s := range spans {
		if s.Name != "quest.role.gate" {
			continue
		}
		bag := map[string]any{}
		for _, kv := range s.Attributes {
			switch kv.Key {
			case "quest.role.required":
				bag["required"] = kv.Value.AsString()
			case "quest.role.actual":
				bag["actual"] = kv.Value.AsString()
			case "quest.role.allowed":
				bag["allowed"] = kv.Value.AsBool()
			}
		}
		gates = append(gates, bag)
	}
	if len(gates) != 2 {
		t.Fatalf("found %d gate spans; want 2", len(gates))
	}
	for _, g := range gates {
		if g["required"] != "elevated" {
			t.Errorf("required = %v; want elevated", g["required"])
		}
	}
	// Allowed gate: actual=planner, allowed=true.
	// Denied gate: actual=unset (empty role normalized), allowed=false.
	allowed := gates[0]
	denied := gates[1]
	if allowed["actual"] != "planner" || allowed["allowed"] != true {
		t.Errorf("allowed gate = %v", allowed)
	}
	if denied["actual"] != "unset" || denied["allowed"] != false {
		t.Errorf("denied gate = %v", denied)
	}
}

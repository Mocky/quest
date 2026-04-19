package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// TestContentRecordersEmitEvents fires every content recorder while
// CaptureContent is on and asserts the expected event names appear on
// the active span.
func TestContentRecordersEmitEvents(t *testing.T) {
	exp := installInMemoryTracer(t)
	prev := captureContent
	setCaptureContent(true)
	defer setCaptureContent(prev)

	ctx, span := CommandSpan(context.Background(), "create", true)
	RecordContentTitle(ctx, "title")
	RecordContentDescription(ctx, "description")
	RecordContentContext(ctx, "context")
	RecordContentAcceptanceCriteria(ctx, "ac")
	RecordContentNote(ctx, "note")
	RecordContentDebrief(ctx, "debrief")
	RecordContentHandoff(ctx, "handoff")
	RecordContentReason(ctx, "reason")
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans; want 1", len(spans))
	}
	wantNames := []string{
		"quest.content.title",
		"quest.content.description",
		"quest.content.context",
		"quest.content.acceptance_criteria",
		"quest.content.note",
		"quest.content.debrief",
		"quest.content.handoff",
		"quest.content.reason",
	}
	if len(spans[0].Events) != len(wantNames) {
		t.Fatalf("got %d events; want %d", len(spans[0].Events), len(wantNames))
	}
	for i, want := range wantNames {
		if spans[0].Events[i].Name != want {
			t.Errorf("event[%d] = %s; want %s", i, spans[0].Events[i].Name, want)
		}
	}
}

// TestContentTruncationApplies confirms each recorder cuts oversized
// values per OTEL.md §4.5 limits.
func TestContentTruncationApplies(t *testing.T) {
	exp := installInMemoryTracer(t)
	ctx, span := CommandSpan(context.Background(), "create", true)

	long2k := strings.Repeat("a", 2000)
	RecordContentTitle(ctx, long2k)       // limit 256
	RecordContentDescription(ctx, long2k) // limit 1024
	RecordContentNote(ctx, long2k)        // limit 512
	span.End()

	wantLens := map[string]int{
		"quest.content.title":       256,
		"quest.content.description": 1024,
		"quest.content.note":        512,
	}
	for _, ev := range exp.GetSpans()[0].Events {
		want, ok := wantLens[ev.Name]
		if !ok {
			continue
		}
		for _, kv := range ev.Attributes {
			if kv.Key == "value" && len(kv.Value.AsString()) != want {
				t.Errorf("%s value len = %d; want %d", ev.Name, len(kv.Value.AsString()), want)
			}
		}
	}
}

// TestContentDisabledNoEvents pins the §4.5 contract: when callers gate
// on CaptureContentEnabled() and the flag is false, no content events
// reach the span.
func TestContentDisabledNoEvents(t *testing.T) {
	exp := installInMemoryTracer(t)
	prev := captureContent
	setCaptureContent(false)
	defer setCaptureContent(prev)

	ctx, span := CommandSpan(context.Background(), "create", true)
	if CaptureContentEnabled() {
		RecordContentTitle(ctx, "should not fire")
	}
	span.End()
	for _, ev := range exp.GetSpans()[0].Events {
		if strings.HasPrefix(ev.Name, "quest.content.") {
			t.Errorf("found content event with capture disabled: %s", ev.Name)
		}
	}
}

// suppress unused import warnings if recorder.go drops trace later
var _ = trace.SpanContextFromContext

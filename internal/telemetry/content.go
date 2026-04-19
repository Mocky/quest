package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Content event recorders per OTEL.md §4.5. Callers gate on
// CaptureContentEnabled() at the call site so the no-op path never
// pays for the truncation helper or the attribute build (§14.2 / §14.5).
// Recorders themselves do not re-check the flag — they accept that the
// caller has already passed the gate.
//
// Truncation limits per §4.5:
//
//   - title:                256
//   - description / context / debrief / handoff: 1024
//   - acceptance_criteria / note / reason:        512

const (
	contentTitleMax       = 256
	contentDescriptionMax = 1024
	contentContextMax     = 1024
	contentACMax          = 512
	contentNoteMax        = 512
	contentDebriefMax     = 1024
	contentHandoffMax     = 1024
	contentReasonMax      = 512
)

// recordContent is the shared body — checks span recording and emits
// one event with a single value attribute.
func recordContent(ctx context.Context, eventName, valueKey, raw string, max int) {
	span := trace.SpanFromContext(ctx)
	if nonRecording(span) {
		return
	}
	span.AddEvent(eventName, trace.WithAttributes(
		attribute.String(valueKey, Truncate(raw, max)),
	))
}

// RecordContentTitle emits a `quest.content.title` span event.
func RecordContentTitle(ctx context.Context, title string) {
	recordContent(ctx, "quest.content.title", "value", title, contentTitleMax)
}

// RecordContentDescription emits a `quest.content.description` span event.
func RecordContentDescription(ctx context.Context, description string) {
	recordContent(ctx, "quest.content.description", "value", description, contentDescriptionMax)
}

// RecordContentContext emits a `quest.content.context` span event.
func RecordContentContext(ctx context.Context, contextText string) {
	recordContent(ctx, "quest.content.context", "value", contextText, contentContextMax)
}

// RecordContentAcceptanceCriteria emits a `quest.content.acceptance_criteria` span event.
func RecordContentAcceptanceCriteria(ctx context.Context, ac string) {
	recordContent(ctx, "quest.content.acceptance_criteria", "value", ac, contentACMax)
}

// RecordContentNote emits a `quest.content.note` span event.
func RecordContentNote(ctx context.Context, note string) {
	recordContent(ctx, "quest.content.note", "value", note, contentNoteMax)
}

// RecordContentDebrief emits a `quest.content.debrief` span event.
func RecordContentDebrief(ctx context.Context, debrief string) {
	recordContent(ctx, "quest.content.debrief", "value", debrief, contentDebriefMax)
}

// RecordContentHandoff emits a `quest.content.handoff` span event.
func RecordContentHandoff(ctx context.Context, handoff string) {
	recordContent(ctx, "quest.content.handoff", "value", handoff, contentHandoffMax)
}

// RecordContentReason emits a `quest.content.reason` span event.
// Used by `quest cancel --reason` and `quest reset --reason`.
func RecordContentReason(ctx context.Context, reason string) {
	recordContent(ctx, "quest.content.reason", "value", reason, contentReasonMax)
}

// Package batch implements the four-phase validator behind `quest
// batch`: parse → reference → graph → semantic. Each phase emits
// structured JSONL errors to stderr when it fails; a clean phase
// commits the full set atomically via store.BeginImmediate. Phase 7
// (Task 7.3) ships the validator and the `invalid_link_type` error
// code added during plan review. Imports store, ids, input, deps.
// Never imports OTEL — emits telemetry via internal/telemetry/ spans
// (quest.batch.parse / .reference / .graph / .semantic) per OTEL.md
// §4.1. See quest-spec.md §`quest batch`.
package batch

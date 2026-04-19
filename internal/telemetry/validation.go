package telemetry

// validation.go lands in Task 12.6 (the `quest.validate` parent span
// and `quest.batch.{parse,reference,graph,semantic}` phase children
// per OTEL.md §8.5). Present in Phase 2 to lock in the §8.1 file
// inventory; carries no symbols that Phase-2 callers invoke.

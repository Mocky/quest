// Package export writes the human-readable archival layout `quest
// export` produces: per-task JSON, debrief markdown, and the
// chronological history.jsonl stream. Export is the archival format,
// the database is the operational format — this package is how the
// "substrate is disposable" grove principle becomes concrete for
// quest (AGENTS.md §Key design decisions). Re-running overwrites the
// output directory and deletes files for tasks that no longer exist
// (cross-cutting.md §Deliberate deviations). Phase 11 ships the
// implementation. Imports store, output. See quest-spec.md §`quest
// export`.
package export

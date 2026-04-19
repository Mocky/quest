// Package output renders command results to stdout. JSON output uses
// struct tags with every field emitted — `null`, `[]`, `{}` for empty
// values per cross-cutting.md §JSON field presence — so the agent-facing
// contract (STANDARDS.md §CLI Surface Versioning) is preserved. Text
// output uses tabwriter with column widths chosen from TTY detection
// (golang.org/x/term) or a fixed width when piped. `--color` is not a
// v0.1 flag; text rendering is plain. Planned exports: RenderJSON,
// RenderText, and per-command helpers. Phase 4 (Task 4.3) lands the
// implementation. See quest-spec.md §Output & Error Conventions.
package output

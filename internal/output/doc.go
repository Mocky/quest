// Package output renders command results to stdout. Exports:
//
//   - Emit(w, format, value)  — the generic structured emitter. JSON
//     mode writes compact output with one trailing newline; text mode
//     is a best-effort %v fallback for simple scalars. Handlers that
//     render structured text (list, graph) call Table / Tree directly
//     and compose their own output.
//   - EmitJSONL[T] / NewJSONLEncoder — batch ref→id stdout (bounded
//     uniform) and batch error stream (heterogeneous per-line field
//     sets). The slice form wraps the encoder so the two cannot drift
//     on quoting / trailing-newline / UTF-8.
//   - OrderedRow — preserves column order for quest list --columns;
//     json.Marshal on a map[string]any alphabetizes keys, so a custom
//     MarshalJSON is the only way to honor the spec §quest list row-
//     shape rules.
//   - Table / Tree — text-mode helpers. Table truncates cells with a
//     trailing "..." on a rune boundary per spec §Text-mode
//     formatting. Phase 10 layers TTY-aware widths on top.
//
// Every structure emitted via Emit honors cross-cutting §JSON field
// presence: `null` for nullable fields, `[]` for empty slices, `{}`
// for empty maps — never omitted. --color is not a v0.1 flag; text
// rendering is plain. See quest-spec.md §Output & Error Conventions.
package output

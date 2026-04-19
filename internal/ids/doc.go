// Package ids validates prefix format, parses task id structure, and
// allocates short task ids per quest-spec §Task IDs. Pure parsing
// (Depth, Parent, ValidateDepth, formatBase36) has no internal
// dependencies; the tx-bound allocators NewTopLevel and NewSubTask
// import internal/store so counter SQL runs inside the caller's
// BeginImmediate transaction. Callers (create / batch / move) own
// depth validation via ValidateDepth before reaching the allocators.
// No OTEL imports — the store's tx-span observability covers allocator
// calls transparently.
package ids

// Package ids validates prefix format and generates the short task IDs
// quest-spec.md §Task ID assignment defines. IDs are built from the
// workspace prefix plus a per-project sequence suffix; planner and batch
// handlers call this package when creating tasks. No internal
// dependencies; no OTEL imports. Planned exports: ValidatePrefix(s),
// ValidateID(prefix, id), NewID(prefix, seq). The generator's uniqueness
// contract lives with the store (sequence allocation is tx-serialized),
// not here. See quest-spec.md §Prefix validation and §Task ID assignment.
package ids

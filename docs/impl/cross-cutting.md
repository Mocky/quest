# Cross-cutting concerns

Back to [manifest](../implementation-plan.md).

These rules apply to every phase. When a phase file says "per cross-cutting", the referent is this document.

## History recording

Every *state-changing* mutation writes exactly one history row; never batch. Idempotent no-ops (duplicate link add, missing link/tag remove, duplicate PR add) write no state change and emit no history, per spec §Idempotency ("returns exit 0 with no state change"). The `action` enum and action-specific payload shape are defined in `quest-spec.md` §History field — implement once in `store.AppendHistory(ctx, tx, History) error` and call it from every write path; write-path code decides whether `RowsAffected > 0` before calling it.

`AppendHistory` converts empty `role` and `session` strings to `sql.NullString{}` at write time so they persist as SQL `NULL` rather than `""` — spec §History field: "Recorded as `null` if unset." The JSON output path uses `*string` on the Go struct so `encoding/json` emits `null` natively without a helper. Writing correctly at the source keeps direct-SQL inspection accurate and the contract-test `TestHistoryEntryShape` (Task 13.1) green without round-trip coercion.

## Nullable TEXT columns

Every nullable TEXT column on `tasks` that corresponds to a JSON `null`-when-unset field is written with `sql.NullString{String: s, Valid: s != ""}` when the source Go string is empty. This covers `owner_session`, `handoff`, `handoff_session`, `handoff_written_at`, `role`, `tier`, `acceptance_criteria`, `parent`, `debrief`, and every `history.role` / `history.session`. The rule lives with `AppendHistory` for history and with each handler's UPDATE for task-row writes (see Task 6.2 for the accept example). Do not retrofit this at the read side — direct SQLite inspection must see `NULL`, not `''`.

## Timestamps

All timestamps are written as `time.Now().UTC().Format(time.RFC3339)` — second precision, UTC, Z-terminated. Applies to `started_at`, `completed_at`, `handoff_written_at`, every `history.timestamp`, every `notes.timestamp`, and the PR `added_at`. Spec §Output & Error Conventions. Sub-second precision is intentionally not used: the single-writer model makes collisions at second precision unlikely, and uniform second precision keeps downstream parsing simple.

## JSON field presence

Every struct that marshals to command output uses explicit `json:"..."` tags and emits `null` / `[]` / `{}` for empty values — never omit. Add a contract test for every command output; the set is non-negotiable per `STANDARDS.md` §CLI Surface Versioning.

## Error messages

User-facing stderr lines: `quest: <class>: <actionable message>` followed by `quest: exit N (<class>)`. The slog record carries the wrapped error. Never leak SQL, file paths from internal sources, or type names to stderr. See `OBSERVABILITY.md` §Sanitization.

## `@file` input

Any flag listed in `quest-spec.md` §Input Conventions goes through an `*input.Resolver` that **each handler constructs at entry** (`r := input.NewResolver(stdin)`). Handlers call `r.Resolve("--debrief", raw)` to expand `@file` / `@-` / bare-string inputs. Adding new flags that accept free-form text? Add them to the spec list and call `r.Resolve` for them in the handler. The "one handler per invocation" property means the "one resolver per invocation" invariant already holds without adding the resolver to the handler signature — revisit if handlers ever share per-invocation state (a second resolver, a rate limiter, etc.).

The `*input.Resolver` keeps per-invocation state: once `@-` has been resolved for one flag, a second `@-` on the same invocation returns `ErrUsage` (exit 2) with `"stdin already consumed by <first-flag>; at most one @- per invocation"` — this rule is spec-owned (`quest-spec.md` §Input Conventions) because agent retry logic depends on the contract. Stdin is a single byte stream; consuming it twice yields empty content or a block on the second read, and silent corruption is worse than an explicit rejection. Tests exercising the second-`@-` rejection, oversized-file, missing-file, and binary-content paths live in `internal/input/resolve_test.go` (one central suite, not distributed across handler tests).

## Telemetry call sites

`cli.Execute` owns `CommandSpan` / `WrapCommand` per `OTEL.md` §8.2 — command handlers do not call either. Handlers receive a context that already carries the command span and call `telemetry.RecordX` at every observable event (status transition, link add/remove, batch outcome, query result count, precondition failure, cycle detection). Per the M5 decision there is no general-purpose `SpanEvent` helper; every span event ships through a named recorder in the §8.6 inventory. Handlers never import `go.opentelemetry.io/otel/trace` or `go.opentelemetry.io/otel/attribute` — the Task 2.3 grep tripwire enforces this. The no-op stubs make these calls safe during Phase 2–11; Phase 12 lights them up. Do not gate calls on a telemetry-enabled check — the `enabled()` helper is package-private to `internal/telemetry/` (`OTEL.md` §8.3), and the no-op SDK providers already make the hot path cheap.

## Precondition-failed events (`OTEL.md` §13.3)

Every exit-5 path in every handler must emit `quest.precondition.failed` via `telemetry.RecordPreconditionFailed(ctx, precondition string, blockedByIDs []string)`. The `precondition` argument is a bounded enum: `children_terminal`, `parent_not_open`, `ownership`, `from_status`, `existence`, `type_transition`, `cycle`, `depth_exceeded`, `cancelled`, `move_history_accepted`, `move_parent_accepted`, `leaf_direct_close` (introduced by C3). Handlers populate `blockedByIDs` only when the precondition is structurally about other tasks (`children_terminal` lists the non-terminal child IDs; `cycle` lists the cycle path; otherwise nil/empty). The recorder applies the §13.3 truncation limits (≤ 10 IDs, ≤ 256 chars total) via the shared `truncateIDList` helper. Affected handlers (must include a `RecordPreconditionFailed` call on every exit-5 path):

- Task 6.2 (`accept`) — `from_status` (non-open accept), `children_terminal` (parent with non-terminal children).
- Task 6.3 (`update`) — `from_status` (terminal-state gating), `cancelled` (cancelled-task rejection), `type_transition` (`--type task` blocked by outgoing `caused-by`/`discovered-from` link), `ownership`.
- Task 6.4 (`complete`/`fail`) — `from_status`, `children_terminal`, `cancelled`, `ownership`, `leaf_direct_close` (C3).
- Task 7.1 (`create`) — `parent_not_open`, `depth_exceeded`, plus per-edge `cycle` / `blocked_by_cancelled` / etc. via `deps.ValidateSemantic`.
- Task 8.1 (`cancel`) — `from_status` (terminal-state cancel rejection).
- Task 8.3 (`move`) — `move_history_accepted`, `move_parent_accepted`, `parent_not_open`, `depth_exceeded`, cycle (circular parentage).
- Task 9.1 (`link`) — `cycle`, semantic constraint violations.

Without these events, dashboards lose the per-precondition breakdown and the §13.3 "trace-first vs log-first debugging" duality breaks. The `TestPreconditionFailedEventShape` contract test (Task 13.1) iterates the exit-5 inventory and asserts the event fires with the matching enum value on every handler.

## Slog event emission (`OTEL.md` §3.2)

Every handler that emits a slog record uses one of the canonical message strings + attribute sets pinned in `OTEL.md` §3.2 — do not invent ad-hoc messages. The eight categories the inventory defines and the call sites in this plan that emit them:

| §3.2 category | Severity | Emitted by |
|---|---|---|
| `quest command start` / `quest command complete` | DEBUG | dispatcher (Task 4.2 step 2 / pre-return) |
| `role gate denied` | INFO | dispatcher (Task 4.2 step 3) and `update` mixed-flag gate (Task 6.3) |
| `BEGIN IMMEDIATE acquired` / `tx committed` / `tx rolled back` | DEBUG | `internal/store/` per-tx bookends (Task 3.1) |
| `precondition failed` | INFO | every handler with an exit-5 path (mirrors `RecordPreconditionFailed` call sites above) |
| `dep cycle detected` | WARN | `deps.ValidateSemantic` callers (Tasks 7.3 batch graph phase, 9.1 link) |
| `batch validation error` | WARN | Task 7.3's batch handler — emit one slog record per stderr JSONL error |
| `batch mode fallthrough` | INFO | Task 7.3's `--partial-ok` partial-success path |
| `write lock timeout` | WARN | `internal/store/` `BeginImmediate` exit-7 path (Task 3.1) |
| `schema migration applied` | INFO | Task 3.2's migration runner; attribute set `schema.from`, `schema.to`, `applied_count` |
| `internal error` | ERROR | dispatcher (Task 4.2, via `telemetry.RecordDispatchError`) AND non-dispatcher panic recovery / unexpected handler errors. Single canonical message per §3.2. Optional `origin="dispatch"\|"handler"` attribute distinguishes the two sources for retrospectives |
| `otel internal error` | WARN | `otel.SetErrorHandler` route (Task 12.1) |

Use `telemetry.Truncate` (256 chars) on any `err` field per `OBSERVABILITY.md` §Standard Field Names. Add a Layer 2 contract test (`TestSlogEventInventory`) that captures slog records during a synthetic invocation per category and asserts the message + attribute set match the §3.2 inventory — this prevents drift as new handlers add slog records.

## Schema evolution

Any change to the DB shape is a numbered migration. Bump `schema_version`. Add a `migration_test.go` fixture at the new version. Never edit an existing migration — the binary's supported-version set is forward-only.

## Duration calculation

Durations recorded on spans and metrics use `float64(elapsed.Microseconds()) / 1000.0`, never `elapsed.Milliseconds()`. The latter truncates sub-millisecond durations to 0, which destroys p50 / p95 signal on fast commands (every quest command that doesn't hit disk finishes in single-digit milliseconds). `OTEL.md` §19 checklist pins this; the plan mirrors the rule here so every duration-emitting call site follows it.

## Integration build tags

Any test file exercising the store, goroutines, or the built binary uses `//go:build integration` per `TESTING.md` §Integration Build Tags. Tasks 5.1, 6.x, 7.x, 8.x, 9.x, 10.x, 11.1 describe Layer 3 / 4 / 5 tests without always restating the build-tag requirement — treat this cross-cutting rule as authoritative.

## Deliberate deviations from spec

Some plan decisions tighten or extend the spec. Track them here so a future reader inspecting divergence can find the rationale in one place. Revisit each entry when its "revisit if" condition is triggered:

- **Unknown `--columns` / `--status` / `--type` / `--tier` values on `quest list` are rejected with exit 2** (Task 10.2). Spec is silent on unknown-value handling for these filter flags; rejecting at parse time with a `cli.Suggest`-powered "did you mean" hint prevents silent partial output (unknown column) and silent empty-result typos (unknown status/type/tier — e.g., `--status compelete` returning `[]` instead of an error). Revisit if agents need forward-compatible filter specs.
- **`--help` is gated by workspace and role checks** (Task 4.2). Spec is silent; plan retains gating rather than following common CLI convention (git / kubectl / docker all short-circuit `--help`). Rationale: quest is agent-first, and a worker asking for help on a command it cannot execute indicates a calling-code bug — exit 6 (role denied) or exit 2 (no workspace) gives the agent an actionable signal that matches what the command itself would return, instead of usage text the agent will never act on. `quest help <cmd>` and `quest --help` remain gate-free for human discovery (role-filtered banner). Revisit if human-operator workflows surface real friction.
- **`quest export` deletes stale output files** (Task 11.1). Spec §`quest export` says "re-running overwrites the output directory." Plan extends this to remove files for tasks that no longer exist (moved via `quest move`, cancelled and recreated, etc.) so the archive is a true snapshot. Revisit if a concrete workflow relies on stale files surviving.
- **`invalid_link_type` batch error code** (Task 7.3 / spec §Batch error output). Added as a new code at phase `semantic` so typos in `link_type` produce a clear diagnostic instead of falling through to `source_type_required`. Spec has been amended; this entry exists for audit trail.
- **Empty `--reason` on cancel / reset records `null`** (Tasks 8.1 / 8.2). Spec is silent; plan treats empty value as equivalent to omitting the flag, asymmetric with `quest update` where empty strings are exit-2 errors. Rationale: `--reason` annotates a state transition rather than attaching task data. Spec has been amended with this behavior.
- **Non-owning worker `update` on open tasks is permitted** (Task 6.3). Spec §`quest accept` says "after acceptance, only the owning session (or an elevated role) can call `quest update` ... on the task" — implying pre-acceptance the ownership check does not apply. Plan allows any worker session to call `quest update --note` / `--handoff` / `--meta` / `--pr` on an `open` task (ownership check runs only on `accepted` tasks). Rationale: the accept-before-update flow is the designed path, but a worker with `AGENT_TASK` set on a not-yet-accepted task is a plausible pre-accept state (e.g., vigil has assigned the task but the worker wants to record a startup note before calling `accept`). Tightening pre-acceptance would require a spec change and introduce a worker-surface policy decision beyond the M9 scope. Revisit if retrospective queries surface confusing attribution (notes from a session that never accepted).
- **Whitespace-only `--debrief` is accepted, literal empty string is rejected** (Task 6.4). Spec §`quest complete` / `quest fail` say "debrief is required" and spec §`quest update` pins "Empty values are usage errors" (exit 2). Plan rejects literal `""` (matching the spec rule for sibling free-form flags) but passes through whitespace-only values as legal debrief content. Per the M10 decision, this is a deliberate narrowing: "required" means non-empty-byte-string, not non-whitespace content. Revisit if retrospectives show planners/workers submitting whitespace-only debriefs to satisfy the required-flag check.

## Agent discipline

If the spec is silent on a question you need answered, stop and resolve it in the spec first. Do not guess. Do not delete or rename existing error classes, exit codes, or JSON fields without a deprecation cycle and a `CHANGELOG.md` entry.

# Phase 8 — Task management

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 8.1 — `quest cancel`

**Deliverable:** `internal/command/cancel.go`.

**Spec anchors:** `quest-spec.md` §`quest cancel` (with and without `-r`), §In-flight worker coordination.

**Implementation notes:**

- Without `-r`: `s.BeginImmediate(ctx, store.TxCancel)`. The precondition check (no non-terminal children) is multi-row, which is why we still use `BEGIN IMMEDIATE` — but the tx touches a single row, so the single-row `cancel` label is the correct dashboard signal.
- With `-r`: `s.BeginImmediate(ctx, store.TxCancelRecursive)` — the enum keeps `cancel_recursive` distinct because the lock-wait profile differs materially from a single-row `cancel` (`OTEL.md` §5.3). Recursive descendant walk; transition `open` and `accepted` descendants to `cancelled`; record skipped (already-terminal) descendants. Report both sets in the response. `-r` on a leaf task (no descendants) proceeds normally per spec §`quest cancel`: `cancelled` contains the target, `skipped` is `[]`.
- **Existence check first.** Inside the transaction, `tx.QueryRow("SELECT status, tier, role, type FROM tasks WHERE id=?", id).Scan(...)`. `sql.ErrNoRows` → `ErrNotFound` (exit 3) — same precondition-ladder pattern as Task 6.2 / 6.4. Without this explicit step, a missing target would surface as a `not_found` from a downstream UPDATE with a less precise classification.
- Idempotent on already-cancelled (exit 0). Rejects `complete` / `failed` (exit 5 — terminal states are permanent).
- `--reason` is optional and goes through the handler-constructed `*input.Resolver` per spec §Input Conventions (supports `@file` and `@-`). Empty value (`--reason ""`) is equivalent to omitting the flag per spec §`quest cancel`; history records `reason: null` in both cases.
- **Skipped descendants with `-r`** include every descendant already in a terminal state (`complete`, `failed`, OR `cancelled`) per spec §`quest cancel`. Emit one `{"id": "<id>", "status": "<status>"}` entry per skipped descendant in the `skipped` array; the caller distinguishes freshly-cancelled from previously-cancelled by which array a descendant appears in.
- History: `cancelled` with `reason` in the payload (reason field is part of the spec per §History field; quest-spec has been updated to list `reason` on `cancelled` alongside `reset`).
- Do not signal vigil or any external system; worker termination is out of scope per spec.
- **Stdout shape** per spec §`quest cancel`: `{"cancelled": [...], "skipped": [...]}`, both arrays always present (empty allowed).
- **Telemetry wiring** (Phase 12): after loading the target task row, call `telemetry.RecordTaskContext(ctx, targetID, tier, taskType)` so the command span carries the §4.3 task-affecting attributes (per H3 — every task-affecting handler must call this). On a real cancel (at least one task transitioned), call `telemetry.RecordCancelOutcome(ctx, targetID, recursive, cancelledCount, skippedCount)` per Task 12.10 and `OTEL.md` §8.6. **For each task transitioned to `cancelled` (root + every non-terminal descendant under `-r`), fire `telemetry.RecordStatusTransition(ctx, descendantID, fromStatus, "cancelled")` and `telemetry.RecordTerminalState(ctx, descendantID, descendantTier, descendantRole, "cancelled")` once per task** — both calls go inside the descendant-walk loop, not once for the root. This is what makes `dept.quest.status_transitions{from, to=cancelled}` and `dept.quest.tasks.completed{outcome=cancelled}` reflect each transition rather than only the root, which is the correctness contract for the "tasks created vs completed" retrospective. Skipped descendants (already terminal) are not transitioned and are not counted. **Idempotent no-op (already-cancelled root, no descendants transitioned, `cancelledCount==0 && skippedCount==0`):** skip `RecordCancelOutcome`, `RecordStatusTransition`, and `RecordTerminalState` entirely — same rule as link/tag idempotency. If `--reason` is supplied and `CaptureContentEnabled()` returns true, call `telemetry.RecordContentReason(ctx, reason)`.

**Tests:** Layer 3: all four before-states, `-r` on a multi-level tree, idempotency on already-cancelled. `TestCancelRecursiveCountersFireOncePerDescendant` asserts that `cancel -r` against a 4-level fixture fires `RecordStatusTransition` and `RecordTerminalState` exactly once per non-terminal descendant transitioned (and not for skipped already-terminal descendants). Layer 2 contract test asserts that an already-cancelled-root invocation produces no recorder calls (matches the M8 idempotency carve-out).

**Done when:** a cancelled task, when later `quest update`d by a worker, returns the structured conflict body per spec §In-flight worker coordination.

---

### Task 8.2 — `quest reset`

**Deliverable:** `internal/command/reset.go`.

**Spec anchors:** `quest-spec.md` §`quest reset`, §Crash Recovery.

**Implementation notes:**

- Route through `s.BeginImmediate(ctx, store.TxReset)` — the `tx_kind` enum has a dedicated `reset` value (`OTEL.md` §4.3), so dashboards track `reset` separately from `accept`. The transaction shape is the same as accept: SELECT to distinguish not-found from wrong-status, then UPDATE. Do not use the atomic-UPDATE shortcut; same rationale as Task 6.2 (must distinguish exit 3 from exit 5).
- **Existence check first.** Inside the transaction, `tx.QueryRow("SELECT status, tier, role, type FROM tasks WHERE id=?", id).Scan(...)`. `sql.ErrNoRows` → `ErrNotFound` (exit 3) — same precondition-ladder pattern as Tasks 6.2 / 6.4 / 8.1. Without this explicit step, a missing target would surface as ambiguous between not-found and wrong-status.
- Missing task → exit 3. Task exists but not in `accepted` status → exit 5.
- On success: `UPDATE tasks SET status='open', owner_session=NULL, started_at=NULL WHERE id=?`. Preserve `handoff`, `handoff_session`, `handoff_written_at`, `notes` — the next session inherits them.
- `--reason` is optional and goes through the handler-constructed `*input.Resolver` per spec §Input Conventions (supports `@file` and `@-`). Empty value (`--reason ""`) is equivalent to omitting the flag per spec §`quest reset`; history records `reason: null` in both cases.
- History: `reset` with `reason` in the payload.
- **Stdout on success** per spec §Write-command output shapes: `{"id": "<id>", "status": "open"}`. Both fields always present; `status` is the literal string `"open"` on success.
- **Telemetry wiring** (Phase 12): after loading the task row, call `telemetry.RecordTaskContext(ctx, id, tier, taskType)` so the command span carries the §4.3 task-affecting attributes (per H3). On success call `telemetry.RecordStatusTransition(ctx, id, "accepted", "open")` to feed `dept.quest.status_transitions` (per H2 / `OTEL.md` §16 step 8 — every status-changing handler emits this). Reset is non-terminal, so `RecordTerminalState` is **not** called. If `--reason` is supplied and `CaptureContentEnabled()` returns true, call `telemetry.RecordContentReason(ctx, reason)`.

**Tests:** Layer 3: accepted → open + preserved handoff; non-accepted → exit 5.

**Done when:** the worker-crash test from `quest-spec.md` §Crash Recovery round-trips: accept, handoff, reset, re-accept by a new session, handoff visible on `show`.

---

### Task 8.3 — `quest move ID --parent NEW_PARENT`

**Deliverable:** `internal/command/move.go`.

**Spec anchors:** `quest-spec.md` §`quest move` — every constraint in the Constraints list.

**Implementation notes:**

- Hardest command. Read the spec twice before writing code.
- `s.BeginImmediate(ctx, store.TxMove)`. Preconditions (fail with exit 5, collecting all applicable messages):
  - The moved subgraph has no `accepted` action in history (for _any_ task in the subgraph, ever — check the history table, not the current status).
  - The moved task's current parent is not in `accepted` status.
  - `NEW_PARENT` is in `open` status.
  - No circular parentage: `NEW_PARENT` is not the moved task or any of its descendants.
  - The resulting depth of the deepest descendant ≤ 3.
- **Rename algorithm, cascade-driven.** Compute the new root ID via `ids.NewSubTask(ctx, tx, NEW_PARENT)`; for every descendant, derive the new ID by swapping the old prefix for the new. Wrap the cascade loop with `ctx2, end := telemetry.StoreSpan(ctx, "quest.store.rename_subgraph"); defer func() { end(err) }()` so the span captures the cascade UPDATE pass per `OTEL.md` §4.2 (the decorator no longer emits this span; handlers do — see Task 12.4). Then for each task in the moved subgraph (root first, then descendants by depth), run a single `UPDATE tasks SET id=?, parent=? WHERE id=?`. The `ON UPDATE CASCADE` FKs on `history`, `dependencies` (both `task_id` and `target_id`), `tags`, `prs`, and `notes` (see Task 3.2 schema) propagate the new `id` to every side table automatically in the same transaction — no manual cross-table UPDATEs, and the history-FK carve-out in spec §History field authorizes it.
- **No `defer_foreign_keys` pragma is needed.** Under quest's current schema, every intermediate state during the rename sequence is already FK-consistent: `ON UPDATE CASCADE` rewrites every referencing row atomically as part of the triggering `UPDATE tasks SET id=?`, so no transient FK violation exists to defer. `defer_foreign_keys` defers *validation*, not cascade actions — cascades fire immediately regardless of the pragma. An earlier plan draft invoked `PRAGMA defer_foreign_keys = ON` "defensively"; per the H15 decision this has been removed to avoid a false signal to future maintainers that the pragma is load-bearing. The `TestMoveSubgraphFKIntegrity` Layer-3 test (below) pins the invariant: a 3-level subgraph move followed by `PRAGMA foreign_key_check` returns zero violations. If a future schema change (new triggers, manual cross-table UPDATEs) introduces a transient FK-violating state, that change must add the pragma with a comment explaining the specific scenario — do not resurrect the pragma defensively.
- (Note: `PRAGMA foreign_keys = OFF` is a no-op inside a transaction and is _not_ the right mechanism; an earlier plan draft had this wrong.)
- Append one `moved` history entry per renamed task with `old_id` / `new_id` in the payload. Updates to dependency references are side-effects of the FK cascade, not their own history entries.
- Output per spec §`quest move`: `{"id": "<new-id-of-moved-task>", "renames": [{"old": "...", "new": "..."}, ...]}`. `renames` is always present, contains at least the moved task itself, and is ordered by old ID ascending. Text mode emits one `OLD → NEW` line per rename. Both fields always present.
- **Computing `depUpdates` for the M16 cascade-count.** The FK-cascade UPDATE pass rewrites `dependencies.task_id` / `dependencies.target_id` rows automatically inside SQLite, but `RowsAffected()` on the triggering `UPDATE tasks` does **not** count cascade side-effects. Inside the transaction, before the rename pass runs, issue `tx.QueryRow("SELECT COUNT(*) FROM dependencies WHERE task_id IN (...) OR target_id IN (...)", movedIDs...)` — this counts the rows that the cascade will rewrite. Both `task_id` and `target_id` are indexed (Task 3.2), so the COUNT runs in O(matched) on a covering index. Pass the resulting integer to `RecordMoveOutcome` as `depUpdates`. Do not switch to manual `UPDATE dependencies SET task_id=? WHERE task_id=?` to use `RowsAffected()` — that bypasses the FK-cascade strategy and reverts a Task 3.2 design decision.
- **Telemetry wiring** (Phase 12): after loading the moved-task row (and computing `subgraphSize` / `depUpdates` per above), call `telemetry.RecordTaskContext(ctx, newID, tier, taskType)` with the **post-rename** `newID` so retrospective queries `quest.task.id = <new-id>` find the move (per H3 — every task-affecting handler must call this; pass the post-rename ID because the old ID no longer exists after commit). On success call `telemetry.RecordMoveOutcome(ctx, oldID, newID, subgraphSize, depUpdates)` per Task 12.10 and `OTEL.md` §8.6 — `oldID`/`newID` are the moved task's own IDs, `subgraphSize` is the count of tasks renamed, `depUpdates` is the count of `dependencies` rows rewritten by the FK cascade computed via the pre-rename COUNT query above.

**Tests:** Layer 3: the full constraint list; subgraph rename round-trip; ID uniqueness after move. `TestMoveSubgraphFKIntegrity` (per H15) — move a 3-level subgraph that includes tasks with `blocked-by` edges, tags, PRs, notes, and history entries; at commit, assert `PRAGMA foreign_key_check` returns zero rows, proving the `ON UPDATE CASCADE` rewrite left no dangling references. The test doubles as the correctness proof for removing `defer_foreign_keys`.

**Done when:** a 3-level subgraph with dependencies moves cleanly and `quest show` on every affected task reflects the new IDs.

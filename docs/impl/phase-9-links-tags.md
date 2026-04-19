# Phase 9 — Links and tags

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 9.1 — `quest link` and `quest unlink`

**Deliverable:** `internal/command/link.go`, `internal/command/unlink.go`.

**Spec anchors:** `quest-spec.md` §Linking, §Multi-type links (uniqueness on `(task, target, type)`), §Dependency validation.

**Implementation notes:**

- `link`: run inside `s.BeginImmediate(ctx, store.TxLink)`; call `deps.ValidateSemantic` (Task 7.2) on the proposed edge. Idempotent on duplicate (task, target, type) via `INSERT OR IGNORE` + `RowsAffected` check.
- `unlink`: run inside `s.BeginImmediate(ctx, store.TxUnlink)`; `DELETE FROM dependencies WHERE task_id=? AND target_id=? AND link_type=?`. Idempotent on missing row.
- **Skip history on idempotent no-ops.** When `RowsAffected == 0` (duplicate add or missing remove), do not append a `linked`/`unlinked` history entry. Spec §Idempotency: "returns exit 0 with no state change" — no state change means no history row. Same rule applies in Task 9.2 for tag/untag.
- History (only when `RowsAffected > 0`): `linked` / `unlinked` with `target` and `link_type` in the payload.
- Default relationship is `--blocked-by` when no flag is provided.
- **Stdout on success** per spec §Write-command output shapes: both commands emit `{"task": "<id>", "target": "<id>", "type": "<link-type>"}` identifying the edge. Same shape on idempotent no-op (the edge that was already present or already absent) — callers cannot distinguish "added now" from "already present" from the success body; the absence of a history row is the distinguishing signal if they care.
- **Telemetry wiring** (Phase 12): after loading the source task row, call `telemetry.RecordTaskContext(ctx, taskID, tier, taskType)` so the command span carries the §4.3 task-affecting attributes (per H3 — every task-affecting handler must call this). When `RowsAffected > 0`, `link` calls `telemetry.RecordLinkAdded(ctx, taskID, targetID, linkType)` and `unlink` calls `telemetry.RecordLinkRemoved(ctx, taskID, targetID, linkType)` (per `OTEL.md` §8.6). On idempotent no-ops (`RowsAffected == 0`), skip the link recorder call — no state change, no event. (`RecordTaskContext` still fires on no-ops because the task identity is observable regardless of edge mutation.) **Cycle path emission:** when `link --blocked-by` returns the `cycle` semantic error, call `telemetry.RecordCycleDetected(ctx, cyclePath)` before returning the exit-5 conflict. The cycle path comes from `deps.ValidateSemantic`'s returned `SemanticDepError.Detail`/`Path`; emit it as a span event per `OTEL.md` §13.4.

**Tests:** Layer 3: each link type, cycle on add (exit 5), duplicate-add no-op, unlink no-op.

**Done when:** all four link types round-trip through link→show→unlink cleanly.

---

### Task 9.2 — `quest tag` and `quest untag`

**Deliverable:** `internal/command/tag.go`, `internal/command/untag.go`.

**Spec anchors:** `quest-spec.md` §Tags.

**Implementation notes:**

- Tags are comma-separated on the command line, normalized to lowercase, stored lowercase. Apply spec §Tags > Validation: `^[a-z0-9][a-z0-9-]*$`, length 1–32, starting with an alphanumeric. Invalid tags → exit 2 naming the offender. Same validator as `quest create --tag` (Task 7.1) and the `tags` field in batch lines (Task 7.3).
- `tag` runs inside `s.BeginImmediate(ctx, store.TxTag)`; `untag` inside `s.BeginImmediate(ctx, store.TxUntag)`. `INSERT OR IGNORE` for add, `DELETE` for remove — both idempotent.
- **Existence check first.** Inside the transaction, `SELECT 1 FROM tasks WHERE id=?`. Zero rows → `ErrNotFound` (exit 3). Without this, `INSERT OR IGNORE INTO tags` would either succeed silently (if FK disabled) or return an FK constraint error that maps less clearly. With the explicit check, error messages cite the missing task ID directly. The FK constraint on `tags.task_id` (Task 3.2) remains as defense-in-depth. Same rule for `untag`: `DELETE` affecting zero rows is ambiguous between "task exists but has no tags" and "task does not exist"; the pre-check disambiguates so the exit code matches spec §Error precedence.
- When `RowsAffected == 0` for a given tag (no-op on add or remove), exclude that tag from the history payload; if every tag in the invocation was a no-op, skip the history append entirely (same rule as Task 9.1 link/unlink).
- History (when at least one tag changed): `tagged` / `untagged` with the effective tag list in the payload.
- **Stdout on success** per spec §Write-command output shapes: both commands emit `{"id": "<id>", "tags": [...]}` where `tags` is the full post-state tag list (sorted alphabetically, lowercased — the canonical form from the `tags` table). Same shape on idempotent no-op (unchanged post-state list).
- **Telemetry wiring** (Phase 12): call `telemetry.RecordTaskContext(ctx, id, tier, taskType)` so the command span carries `quest.task.id` / `quest.task.tier` / `quest.task.type` per `OTEL.md` §4.3. No dedicated `RecordTagAdded` / `RecordTagRemoved` recorder — the cross-cutting `quest.store.tx` span plus the task-context attributes are sufficient observability for tag churn; dashboards that need tag-change counts derive from history.

**Tests:** Layer 3 add + remove + idempotency.

**Done when:** tag management round-trips cleanly; `--tag` filter in `quest list` (Phase 10) matches what `tag` writes.

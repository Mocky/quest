# Phase 10 — Queries

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 10.1 — `quest deps`

**Deliverable:** `internal/command/deps.go`.

**Spec anchors:** `quest-spec.md` §Queries §`quest deps`.

**Implementation notes:**

- Unlike worker commands, `deps` does not default to `AGENT_TASK`. Require an explicit ID; missing → `ErrUsage`.
- Wrap the dependency read with `ctx2, end := telemetry.StoreSpan(ctx, "quest.store.traverse"); defer func() { end(err) }()` so graph/list traversals emit `quest.store.traverse` per `OTEL.md` §4.2 (handler-side emission; the decorator no longer emits this span — see Task 12.4).
- Return dependencies with title and status denormalized (same shape as the `dependencies` array on `quest show`).
- **Telemetry wiring** (Phase 12): on success call `telemetry.RecordQueryResult(ctx, "deps", resultCount, telemetry.QueryFilter{})` per Task 12.9. Pass an **empty** filter — `quest.query.filter.parent` is deliberately not recorded as a span attribute (`OTEL.md` §4.3 excludes parent IDs as high-cardinality), so passing `Parents=[id]` would be a no-op from the telemetry side. The deps target ID is carried by `quest.task.id` via `RecordTaskContext`. Also call `telemetry.RecordTaskContext(ctx, id, tier, taskType)` so the command span carries the mandatory `quest.task.id`, `quest.task.tier`, `quest.task.type` attributes per `OTEL.md` §4.3.

**Tests:** Layer 3 happy path; not-found; zero deps → empty array.

---

### Task 10.2 — `quest list`

**Deliverable:** `internal/command/list.go`.

**Spec anchors:** `quest-spec.md` §Queries §`quest list` — full flag table.

**Implementation notes:**

- Filter composition: every filter flag is AND-combined with every other flag. Semantics _within_ a filter differ by flag per the spec:
  - Enum-style filters (`--status`, `--role`, `--tier`, `--type`, `--parent`): comma-separated values are **OR**; repeated flags union (also OR).
  - `--tag`: comma-separated values are **AND** (a task tagged `go` _and_ `auth`); repeated flags add further AND conditions. Matches `quest create --tag` convention and the spec example.
- This asymmetry is deliberate: enums are mutually-exclusive so OR is the only useful semantics; tags compose multiplicatively so AND is the only useful semantics.
- **Default `--status` filter** (spec §`quest list`): when the user omits `--status`, the handler sets `filter.Statuses = []string{"open","accepted","complete","failed"}` before calling `store.ListTasks` — cancelled tasks are excluded from the default listing. Passing an explicit `--status` that includes `cancelled` (or any subset that omits other statuses) is honored as-is. The defaulting lives in the handler, not in `ListTasks`; the store treats an empty `filter.Statuses` slice as "no status filter" (uniform with the other enum filters). `TestListDefaultStatusExcludesCancelled` (Task 13.1) pins this behavior.
- `--ready` has the trickiest semantics per spec:
  - Leaves: `status == open` AND every `blocked-by` target is `complete`.
  - Parents: `status == open` AND every `blocked-by` target is `complete` AND every child is terminal.
  - Mix leaves and parents in a single response; the presence of `children` tells the caller which is which — request `--columns id,status,children,title` (or similar) to opt in to the distinguisher, since `children` is in the available-columns list but not the defaults.
- Column selection: `--columns` overrides defaults (`id`, `status`, `blocked-by`, `title`). Available columns per spec §`quest list`: `id`, `title`, `status`, `type`, `tier`, `role`, `tags`, `parent`, `blocked-by`, `children`.
- **Unknown column names are rejected with exit 2.** `--columns foo,bar` where any name is not in the available-columns set returns `ErrUsage` (exit 2) with a message naming the first offender and listing the valid names. Silent fall-through on typos is a footgun (`--columns ttitle` would produce rows with missing data); explicit rejection surfaces planner mistakes at the CLI. Task 13.1's `TestListJSONRowShape` matrix adds a case for this rejection.
- **Unknown `--status` / `--type` / `--tier` values are rejected with exit 2.** Same rationale as `--columns`: a planner running `quest list --status compelete` (typo) would otherwise get an empty result and conclude "no complete tasks" — silent footgun. When any value in `--status`, `--type`, or `--tier` is not in the valid set, return `ErrUsage` (exit 2) with a "did you mean" suggestion produced by the shared `cli.Suggest` helper (Task 4.2). Valid sets: `--status` ∈ {`open`, `accepted`, `complete`, `failed`, `cancelled`}; `--type` ∈ {`task`, `bug`}; `--tier` ∈ {`T0`, `T1`, `T2`, `T3`, `T4`, `T5`, `T6`}. Error-body shape mirrors the usage-error pattern: `{"error":"usage","message":"unknown status 'compelete'; did you mean 'complete'?","valid":["open","accepted","complete","failed","cancelled"]}`. When no close match exists (`Suggest` returns ""), drop the "did you mean" clause and emit just the enumeration. Task 13.1 adds `TestListUnknownStatusRejected`, `TestListUnknownTypeRejected`, `TestListUnknownTierRejected`, and `TestListFuzzySuggestion` to pin these.
- JSON output is an array, not JSONL — `list` is a bounded result set. Shape is pinned by spec §`quest list` (row shape rules): keys exactly match the requested columns in `--columns` order, scalars are strings, unset `role` / `tier` / `parent` emit `null` (never `""`), `tags` and `children` are always arrays of strings (possibly empty), `blocked-by` is always an array of task ID strings (not `{id,status,title}` objects — that richer shape belongs to `quest graph`), and a zero-match query emits `[]` (never `null`, never a missing key). Task 13.1 pins these invariants via `TestListJSONRowShape`.
- **Wrap the `ListTasks` call with `quest.store.traverse` only when `--ready` is set.** `OTEL.md` §4.2 scopes the span to graph traversals: `graph`, `deps`, and `--ready` filtering. A plain `quest list --status open` is a single-table predicate scan, not a graph traversal — emitting the span on every list invocation would inflate the `quest.store.traverse` duration histogram with non-traversal rows and break dashboards that assume "traverse duration = graph cost." Concretely:
  ```go
  if filter.Ready {
      ctx2, end := telemetry.StoreSpan(ctx, "quest.store.traverse")
      defer func() { end(err) }()
      ctx = ctx2
  }
  ```
  The non-ready path emits no `quest.store.traverse` child — the list cost rolls up under the command span only. Tests assert `quest list --status open` produces exactly the command span (no traverse child) and `quest list --ready` produces both.
- **Telemetry wiring** (Phase 12): on success call `telemetry.RecordQueryResult(ctx, "list", resultCount, filter)` per Task 12.9. The recorder emits bounded-enum filter attributes and the `quest.query.ready` bool; tag and parent filters are intentionally not mirrored onto span attributes.

**Tests:** Layer 3 matrix — every flag combination has at least one case. `--ready` has its own test covering leaf-ready, leaf-blocked, parent-ready-roleful (dispatch), parent-ready-roleless (direct-close), parent-not-ready (non-terminal children). Additional Layer 1 tests pin the H11 conditional `quest.store.traverse` emission: `quest list --status open` produces exactly one span (the command span) and **no** `quest.store.traverse` child; `quest list --ready` produces both.

---

### Task 10.3 — `quest graph ID`

**Deliverable:** `internal/command/graph.go`.

**Spec anchors:** `quest-spec.md` §Queries §`quest graph` — full JSON shape and "external nodes" semantics.

**Implementation notes:**

- **Explicit ID required.** `quest graph` does not default to `AGENT_TASK` (spec §`quest graph`). Missing ID → `ErrUsage` (exit 2) with `"quest graph requires an explicit task ID"`. Mirrors `quest deps` (Task 10.1) — both are elevated query commands used by planners to inspect a specific subtree.
- Wrap the traversal with `ctx2, end := telemetry.StoreSpan(ctx, "quest.store.traverse"); defer func() { end(err) }()` — graph is the canonical traversal command and the traverse span is emitted handler-side per `OTEL.md` §4.2.
- Traverse from `ID` through `children` (parent-child) and follow outgoing dependency edges.
- Any target reached via a dependency edge that is _not_ a descendant of `ID` is an **external** node: it appears in `nodes` with `children: []` and its own edges are not expanded. Consumers detect it via ID prefix comparison.
- `edges[]` uses quest-specific field names (`task`, `target`, `type`, `target_status`), not generic `source`/`target`.
- Text mode: indented tree per the spec example, with dependency edges listed under the owning task.
- **Telemetry wiring** (Phase 12): on success call `telemetry.RecordGraphResult(ctx, rootID, nodeCount, edgeCount, externalCount, traversalNodes)` per Task 12.9 and `OTEL.md` §8.6. The recorder emits `quest.task.id`, `quest.graph.node_count`, `quest.graph.edge_count`, `quest.graph.external_count` on the span and increments `dept.quest.graph.traversal_nodes` on the metric.

**Tests:** Layer 3: root at epic (full tree); root at leaf (just the leaf + external deps); dep-only cross-prefix external; depth-limited traversal correctness.

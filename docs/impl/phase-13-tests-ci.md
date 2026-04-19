# Phase 13 — Contract, concurrency, and CI tests

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 13.1 — Contract test suite (Layer 2)

Per `TESTING.md` §Layer 2, implement at minimum:

- `TestShowJSONHasRequiredFields` — every field from spec §Task Entity Schema. Missing task ID → exit 3 (pinned here because spec §Error precedence makes it a contract, not a handler detail). Note: `history` is intentionally absent; see `TestShowHistoryFieldPresence`. Uses the shared `testutil.AssertJSONKeyOrder(t, got []byte, want []string)` helper to assert both presence AND order — `json.Marshal` on a Go struct preserves field declaration order today, but a future refactor to `map[string]any` would silently break ordering. The helper catches that.
- `TestShowHistoryFieldPresence` — `quest show` without `--history` omits the `history` field entirely (spec §`quest show` carve-out); with `--history` the field is present and is an array (possibly empty).
- `TestExitCodeStability` — the table from `STANDARDS.md` §CLI Output Contract Tests.
- `TestIdempotencyGuarantees` — every row of the spec's idempotency table.
- `TestHistoryEntryShape` — per-action required fields. Include assertions that `role` and `session` serialize as `null` (not `""`) when the recording session had no `AGENT_ROLE` / `AGENT_SESSION` set (the `AppendHistory` single-site conversion), and that the `created` action's payload captures non-default planning fields per spec §History field.
- `TestBatchStderrShape` — every batch error code with the documented fields, including `invalid_tag` and `invalid_link_type`.
- `TestBatchOutputShape` — `quest batch` stdout JSONL is `{"ref": "...", "id": "..."}` per created task; both fields always present.
- `TestExportLayout` — `tasks/{id}.json`, `debriefs/{id}.md`, `history.jsonl`.
- `TestRoleGateDenials` — iterates the descriptor inventory filtering on `Elevated: true` commands only; asserts each one returns exit 6 for worker role. Adding a new elevated command automatically grows the test. `update` is **skipped** by this iteration because its descriptor has `Elevated: false` at dispatch (workers can call `--note`/`--pr`/`--handoff`). Pin this carve-out with an inline test comment `// update is excluded — see TestUpdateElevatedFlagsDenied` so future refactors do not collapse the two tests.
- `TestUpdateElevatedFlagsDenied` — companion test covering the `update` mixed-flag carve-out: for each elevated flag on `update` (`--title`, `--description`, `--context`, `--type`, `--tier`, `--role`, `--acceptance-criteria`, `--meta`), assert that a worker invoking it produces exit 6. The handler-side gate span (Task 6.3) is exercised here; the dispatcher-side gate test above does not cover it.
- `TestVersionOutputShape` — `quest version --format json` always contains the `version` key; `--format text` emits the bare version string; exit code 0. Also asserts no OTEL span is emitted (version is suppressed at dispatch per `OTEL.md` §4.2).
- `TestInitOutputShape` — `quest init` JSON always contains `workspace` and `id_prefix`, both non-empty; asserts `filepath.Base(workspace) == ".quest"` so the test stays portable across Windows path separators; `--format text` emits the bare absolute `.quest/` path followed by a newline (no prefix, no framing — spec §`quest init`).
- `TestAcceptOutputShape` — `{"id": "...", "status": "accepted"}` on success; both fields present. Uses `testutil.AssertJSONKeyOrder` for stable key order.
- `TestCompleteOutputShape` — `{"id": "...", "status": "complete"}` on success; both fields present. Uses `testutil.AssertJSONKeyOrder`.
- `TestFailOutputShape` — `{"id": "...", "status": "failed"}` on success; both fields present. Uses `testutil.AssertJSONKeyOrder`.
- `TestCreateOutputShape` — `{"id": "<new-id>"}`; single field, no echo of planning args.
- `TestUpdateOutputShape` — `{"id": "..."}`; single field; applies to worker-only and elevated-flag invocations alike.
- `TestLinkOutputShape` / `TestUnlinkOutputShape` — `{"task": "...", "target": "...", "type": "..."}` on both success and idempotent no-op.
- `TestTagOutputShape` / `TestUntagOutputShape` — `{"id": "...", "tags": [...]}` post-state; same shape on idempotent no-op (unchanged list).
- `TestDepsOutputShape` — array of dependency objects matching the `dependencies` array shape on `quest show` (id, type, title, status).
- `TestMoveOutputShape` — `quest move` JSON always contains `id` and `renames`; `renames` is a non-empty array of `{"old","new"}` pairs ordered by old ID.
- `TestCancelOutputShape` — `quest cancel` JSON always contains both `cancelled` and `skipped` arrays (possibly empty); idempotent no-op returns both as `[]`. `-r` on a leaf returns `{"cancelled": [target], "skipped": []}`.
- `TestResetOutputShape` — `quest reset` JSON always contains `id` and `status`; `status` is always the literal `"open"` on success.
- `TestGraphOutputShape` — `quest graph` JSON always contains `nodes` and `edges`; each node has `id`, `title`, `type`, `status`, `tier`, `role`, `children`; each edge has `task`, `target`, `type`, `target_status`. External nodes appear in `nodes` with `children: []`. Missing ID → exit 2 (spec §`quest graph`).
- `TestListJSONRowShape` — rows contain only the requested columns in `--columns` order; `role` / `tier` / `parent` use `null` for unset values (never `""`); `tags` and `children` are always arrays of strings (possibly empty); `blocked-by` is always an array of task ID strings (not object form); empty result is `[]` (never `null`, never missing). Unknown column name → exit 2. Also asserts key ordering explicitly: `quest list --columns title,id` emits `{"title":...,"id":...}` with `title` first — catches any regression that replaces `output.OrderedRow` with a plain `map[string]any` (which would sort keys alphabetically).
- `TestAcceptConflictStructuredBodyOnStdout` — `quest accept` on a parent with non-terminal children emits the structured JSON conflict body (`error`, `task`, `non_terminal_children`) on stdout; exit 5. Exact-match shape (not substring).
- `TestAcceptConflictStderrLineReferencesBlockingChildren` — the same invocation emits a plain-text `quest: conflict: ...` line on stderr that references the same blocking child IDs as the stdout JSON body. Stderr carries the two-liner (`quest: conflict: ...` + `quest: exit 5 (conflict)`), not the JSON. These are two independent contracts — per `OBSERVABILITY.md` §Output Contract, stdout gets the structured body; stderr gets the plain-text summary.
- `TestAcceptOnNonOpenStdoutEmpty` — `quest accept` on a non-open task (one sub-test each for `accepted`, `complete`, `failed`, `cancelled` from-statuses) returns exit 5 with an empty stdout and the standard stderr two-liner (`quest: conflict: task is not in open status (current: <status>)` + `quest: exit 5 (conflict)`). No structured body is emitted on stdout. Pins the H5 carve-out so a future refactor cannot silently introduce an invented conflict body on accept.
- `TestCompleteOnCancelledTaskStructuredBody` / `TestFailOnCancelledTaskStructuredBody` / `TestUpdateOnCancelledTaskStructuredBody` — the cancelled-task conflict body (`{"error":"conflict","task":"...","status":"cancelled","message":"task was cancelled"}`) is emitted on stdout with exit 5. This is the signal vigil uses to terminate workers; silent breakage here breaks vigil integration.
- `TestCompleteOnNonOwnedParentReturnsExit4` / `TestFailOnNonOwnedParentReturnsExit4` — a non-owning worker invoking `complete` / `fail` on a parent they did not accept (but which was accepted by another verifier) returns exit 4 (permission denied). Prevents the "leaves only" ownership-check hole from reappearing after refactors.
- `TestUpdateMetaMergePreservesOtherKeys` — `quest update proj-a1 --meta source=planner`, then `quest update proj-a1 --meta reviewed=true`, then `quest show proj-a1`: both keys present. Additionally, `quest show --history proj-a1` has two `field_updated` entries scoped to metadata, one per invocation, with the correct `from`/`to` deltas.
- `TestUpdateTypeTaskAllowsIncomingCausedByLink` — a `type: bug` source with only **incoming** `caused-by` links (i.e., another task points at it via `caused-by`) can still be retyped to `task`. Pins the outgoing-only predicate from Task 6.3's `--type` transition check so a regression that reintroduces the `OR target_id=?` clause fails the test.
- `TestUpdateRetypeBugToTaskIgnoresIncomingLinks` — companion test: outgoing `caused-by`/`discovered-from` edges still block the retype with exit 5; the exit-5 body's `blocking_links` list includes only the outgoing edges.
- `TestListDefaultStatusExcludesCancelled` — `quest list` with no `--status` flag returns the **full default set** (every task in `open`, `accepted`, `complete`, `failed`) and omits cancelled tasks; passing an explicit `--status open,accepted,complete,failed,cancelled` includes the cancelled rows. Asserting the full default set (not only that cancelled is absent) catches a regression that also drops `failed` from the default. Pins the default-filter contract from Task 10.2.
- `TestRoleUnsetRendering` — a span emitted with empty `AGENT_ROLE` carries `gen_ai.agent.name="unset"`; the matching metric dimension (`role`) carries `"unset"` for the same invocation. Ensures span and metric use a single `roleOrUnset` rendering per `OTEL.md` §8.6.
- `TestLockTimeoutSpanShape` (moved to Task 13.2, `concurrency_test.go`) — a simulated lock timeout (two concurrent writers, one holding the lock past `busy_timeout`) produces a `quest.store.tx` span with `quest.lock.wait_limit_ms=5000` and `quest.lock.wait_actual_ms` ≥ 5000 attributes on the rolled-back transaction per `OTEL.md` §4.3. Exit code is 7 (transient failure). This test requires real concurrent writers, which belongs in Layer 5 per `TESTING.md`; the Layer-2 contract suite stays focused on shape / ordering / idempotency.
- `TestPreconditionFailedEventShape` — a `complete` on a parent with non-terminal children produces a `quest.precondition.failed` span event on the command span with `quest.precondition=children_terminal`, `quest.blocked_by_count=<N>`, and a truncated `quest.blocked_by_ids` attribute per `OTEL.md` §13.3. Anchors handler-side event emission that would otherwise be easy to forget.
- `TestCurrentSchemaVersionTransientError` (moved to Task 13.2, `concurrency_test.go`) — a simulated `SQLITE_BUSY` on the initial `meta` read in `CurrentSchemaVersion` surfaces as exit 7 (`ErrTransient`) per Task 3.1's error-mapping contract; any other driver error surfaces as exit 1 (`ErrGeneral`). Real busy-state requires concurrent writers, hence Layer 5 placement.
- `TestChildSpansOmitGenAIAttributes` — capture spans via the in-memory exporter and iterate **every non-root span** the exporter recorded (rather than an enumeration of expected names). For each, assert that `gen_ai.tool.name`, `gen_ai.operation.name`, and `gen_ai.agent.name` are all absent. Enumerating by "every non-root span captured" keeps the test stable when new child spans are added (e.g., future `quest.validate` sibling spans, additional batch phases); enumerating the full `gen_ai.*` set catches future attribute additions automatically. Current child-span name set for reference: `quest.store.tx`, `quest.role.gate`, `quest.db.migrate`, `quest.validate`, `quest.batch.parse`, `quest.batch.reference`, `quest.batch.graph`, `quest.batch.semantic`, `quest.store.traverse`, `quest.store.rename_subgraph`.
- `TestCommandSpanOnRoleDenial` — a role-denied invocation produces a command span (`execute_tool quest.<cmd>`) with `exit_code=6` / `class=role_denied`, a `quest.role.gate` child span, increments `dept.quest.operations{status=error}`, and increments `dept.quest.errors{error_class=role_denied}`. Gate-denied commands short-circuit at dispatch step 3 (before `cfg.Validate` / `store.Open` / migrate) and record via `errorExit`, which handles both counter increments plus the three-step error pattern on the command span.
- `TestHandlerRecorderWiring` — tripwire iterating the task-affecting handler inventory (`show`, `accept`, `update`, `complete`, `fail`, `create`, `cancel`, `reset`, `move`, `link`, `unlink`, `tag`, `untag`, `deps`, `list`, `graph`, `batch`) and asserting each one emits at least one `telemetry.RecordX` call on its happy path (verified via capturing recorders in `internal/testutil/`). Asserts that the §4.3 task-affecting handlers (`show`, `accept`, `update`, `complete`, `fail`, `cancel`, `reset`, `move`, `deps`, `tag`, `untag`, `graph`) emit `RecordTaskContext` specifically — without this, the §4.3 attribute coverage contract is not enforced. Asserts that status-changing handlers (`accept`, `complete`, `fail`, `cancel`, `reset`) emit `RecordStatusTransition`. Asserts that terminal-state arrivals (`complete`, `fail`, `cancel`) emit `RecordTerminalState`. Asserts that the batch handler emits `RecordBatchOutcome`. Cross-reference: `TestRoleGateDenials` (filters by `Elevated: true`) and `TestUpdateElevatedFlagsDenied` (covers the mixed-flag carve-out) — the three tests together cover the full descriptor inventory. `show` and `reset` were missing from the prior inventory; their inclusion is the M23 fix and L14 follow-up.

**File location.** `internal/command/` is a flat package (`package command`, one file per handler — `accept.go`, `create.go`, …), so per-command contract tests live in a single `internal/command/contract_test.go` organized by `t.Run("<command>", ...)` sub-tests. Flat layout keeps shared unexported helpers (e.g., `CheckOwnership` from H4) accessible without an extra package and avoids 12 one-directory-per-handler sub-packages for quest's small handler surface:

```
internal/command/contract_test.go            One file, all per-command shape tests
                                             organized as t.Run("show", ...), t.Run("accept", ...), etc.
                                             Covers: TestShowJSONHasRequiredFields, TestShowHistoryFieldPresence,
                                             TestAcceptOutputShape, TestAcceptConflictStructuredBody,
                                             TestUpdateOutputShape, TestUpdateOnCancelledTaskStructuredBody,
                                             TestCompleteOutputShape, TestCompleteOnCancelledTaskStructuredBody,
                                             TestFailOutputShape, TestFailOnCancelledTaskStructuredBody,
                                             TestCreateOutputShape, TestLinkOutputShape, TestUnlinkOutputShape,
                                             TestTagOutputShape, TestUntagOutputShape, TestCancelOutputShape,
                                             TestResetOutputShape, TestMoveOutputShape, TestDepsOutputShape,
                                             TestListJSONRowShape, TestGraphOutputShape, TestInitOutputShape,
                                             TestVersionOutputShape
internal/cli/contract_test.go                TestRoleGateDenials, TestUpdateElevatedFlagsDenied,
                                             TestCommandSpanOnRoleDenial,
                                             TestChildSpansOmitGenAIAttributes, TestIdempotencyGuarantees,
                                             TestExportLayout, TestHandlerRecorderWiring
internal/batch/contract_test.go              TestBatchStderrShape, TestBatchOutputShape
internal/batch/deps_test.go                  TestValidateSemanticErrorCodes — unit test
                                             of the SemanticDepError set independent of the batch
                                             stderr contract; lives alongside deps.go which owns
                                             the validator.
internal/store/contract_test.go              TestHistoryEntryShape
internal/errors/contract_test.go             TestExitCodeStability (the exit-code mapping lives
                                             in internal/errors/, so the test lives there too)
internal/input/resolve_test.go               Full @file/@- resolver matrix: happy path, oversized
                                             file, missing file, second @-, binary file — one
                                             central suite for the cross-cutting input layer.
                                             Size-limit boundary cases pin the 1 MiB contract:
                                             exactly 1,048,576 bytes passes; 1,048,577 fails with
                                             exit 2 and the flag-leading error format; the
                                             measurement is byte-exact (post-normalization content
                                             length is NOT what's counted).
internal/telemetry/contract_test.go          TestRoleUnsetRendering (unit-level — assert
                                             roleOrUnset rendering on a synthetic CommandSpan
                                             without handler round-trip)
```

Placement rationale: `TestExitCodeStability` moves to `internal/errors/` because the exit-code table lives there; split the telemetry tests into a unit test (`TestRoleUnsetRendering` stays in `internal/telemetry/`) and a cross-package contract test (`TestChildSpansOmitGenAIAttributes` — the plural-attributes name is authoritative because the body iterates `gen_ai.tool.name`, `gen_ai.operation.name`, and `gen_ai.agent.name`; moves to `internal/cli/` alongside the other handler-round-trip assertions) since the latter needs every command to actually produce spans.

**Done when:** all contract tests pass and any future spec-breaking change trips at least one.

---

### Task 13.2 — Concurrency tests (Layer 5)

Per `TESTING.md` §Layer 5 and `OTEL.md` §15:

- `TestConcurrentAcceptLeavesOnlyOneWinner` (accept race).
- `TestConcurrentCreateGeneratesDistinctIDs` (counter race).
- `TestBusyTimeoutTransientFailure` (5s lock wait, exit 7).
- `TestLockTimeoutSpanShape` — same two-writer setup as `TestBusyTimeoutTransientFailure`, but asserts the `quest.store.tx` span attributes (`quest.lock.wait_limit_ms=5000`, `quest.lock.wait_actual_ms` ≥ 5000) per `OTEL.md` §4.3.
- `TestCurrentSchemaVersionTransientError` — hold a long write transaction, run a second invocation whose `CurrentSchemaVersion` read hits `SQLITE_BUSY`; assert exit 7 (`ErrTransient`) per Task 3.1.
- `TestBulkBatchValidatesInReasonableTime` (500 tasks, dense blocked-by graph — soft perf target).
- `TestBatchCycleRaceConfinedToTransaction` (per H12) — start a batch validation that would close a cycle; in a second goroutine, add an edge mid-validation that would independently close the same cycle. Assert the batch either wins the lock first (and rejects the cycle inside the transaction) OR loses the lock and returns exit 5 with the post-concurrent-edge graph state. Either outcome is correct; the race must not produce a committed batch with an inconsistent graph. Pairs with the H12 decision to run phases 2–4 inside `BeginImmediate`.

All behind `//go:build integration` and run with `-race`.

---

### Task 13.3 — CLI output contract tests (Layer 4)

Build the binary once in `TestMain` (`TESTING.md` §Store Fixtures and Seed Helpers), invoke it via `os/exec` for scenarios that can only be tested end-to-end: global flag positioning, `--format text` rendering, stderr `quest: <class>: <msg>` + `quest: exit N (<class>)` tail, `@file` input.

Include text-mode smoke tests for the commands whose `--format text` output is most likely to drift: `quest cancel` (one-line-per-ID output), `quest move` (`OLD → NEW` rename lines), `quest reset` (bare `<id>` line), and `quest tag` / `quest untag` (post-state tag list). Text mode is not a contract (spec §Output & Error Conventions), but a smoke test guards against a renderer regression making text output silently unusable.

---

### Task 13.4 — CI pipeline

**Deliverable:** `.github/workflows/ci.yml` (or the equivalent for whichever CI the repo ends up using) running:

```bash
go test -race -count=1 -tags integration -coverprofile=coverage.out ./...
```

plus `go build ./...`, `go vet ./...`, and `gofmt -l .`. Per `TESTING.md` §CI Expectations.

# Phase 7 — Planner task creation

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 7.1 — `quest create`

**Deliverable:** `internal/command/create.go`.

**Spec anchors:** `quest-spec.md` §Task Creation, §Parent Tasks §Enforcement rules, §Graph Limits, §Dependency validation.

**Implementation notes:**

- Flags per the spec table. `--title` is required; everything else is optional.
- `--tag` is comma-separated (not repeatable for a single invocation). Apply spec §Tags > Validation: lowercase every tag, then require each to match `^[a-z0-9][a-z0-9-]*$` and be 1–32 characters. Whitespace, `.`, `_`, `/`, or other punctuation → exit 2 naming the offending tag. `quest tag` / `quest untag` (Task 9.2) and the `tags` field in `quest batch` lines (Task 7.3) reuse the same validator.
- `--meta` repeatable, same parsing and JSON re-serialization as `quest update --meta` (Task 6.3). `tasks.metadata` is always valid JSON on write.
- `--parent`: must be in `open` status (spec §Parent Tasks); depth-check — new depth = `Depth(parent)+1`, reject if > 3 with `ErrConflict` citing "depth exceeded".
- Dependency flags (`--blocked-by`, `--caused-by`, `--discovered-from`, `--retry-of`): validated in-process via `deps.ValidateSemantic` (Task 7.2). `--blocked-by` is repeatable (accumulates multiple upstream dependencies); `--caused-by`, `--discovered-from`, and `--retry-of` are single-value flags (spec §`quest create` — each describes one originating event; express multiple origins as `quest link` calls after creation).
- Transaction: every create runs inside `s.BeginImmediate(ctx, store.TxCreate)` — top-level and parented alike. The counter read-modify-write plus row insert share the write lock, and the `quest.store.tx` span is emitted per invocation so dashboards track top-level create timing the same way they track parented creates (`OTEL.md` §4.3 — `tx_kind` covers both cases with a single value). Generate ID inside the tx via `ids.NewTopLevel` or `ids.NewSubTask`.
- Append a `created` history row with the payload shape pinned in spec §History field: captures non-default values of `tier`, `role`, `type` (when ≠ default `task`), `parent`, `tags`, and any initial `dependencies`. Fields left at defaults are omitted from the payload, not serialized as `null`.
- **Stdout on success** per spec §Write-command output shapes: `{"id": "<new-id>"}` — the only field. Callers that need the full task row run `quest show` immediately after. Text mode emits the new ID followed by a newline.
- **Telemetry wiring** (Phase 12): on success call `telemetry.RecordTaskCreated(ctx, id, tier, role, taskType)` — increments `dept.quest.tasks.created{tier, role, type}` per `OTEL.md` §5.1 / §8.6 (the metric dimensions are `tier × role × type`, all three required). Call `telemetry.RecordContentTitle`, `RecordContentDescription`, `RecordContentContext`, `RecordContentAcceptanceCriteria` as applicable for each field supplied, each gated on `CaptureContentEnabled()` per Task 12.7.

**Tests:** Layer 3 full matrix of precondition failures: parent-not-open (exit 5), depth exceeded (exit 5), missing title (exit 2), each dep-validation rule (see Task 7.2 for the table).

**Done when:** a create-one-chain test produces the expected tree and every dependency rule fires the correct exit code.

---

### Task 7.2 — Dependency validator (`internal/batch/deps.go`, shared with batch)

**Deliverable:** `deps.ValidateSemantic(ctx, store, source TaskShape, edges []Edge) []SemanticDepError` — returns every dependency-rule violation, not just the first. The function's scope is strictly dependency-edge semantics (cycle + per-link-type constraints); structural concerns like parse/reference uniqueness and graph depth belong to the batch phases (Task 7.3).

Types used by the signature (declared in `internal/batch/deps.go`):

```go
type TaskShape struct {
    Type string // "task" | "bug" | ... — the only field ValidateSemantic reads
}

type Edge struct {
    Target   string // task ID of the edge target
    LinkType string // "blocked-by" | "caused-by" | "discovered-from" | "retry-of"
}

type SemanticDepError struct {
    Code   string // one of the codes below
    Target string // the edge's target ID
    Type   string // the edge's link_type
    Detail string // free-form diagnostic (e.g., the cycle path)
}
```

**Spec anchors:** `quest-spec.md` §Dependency validation (cycle detection for `blocked-by`, semantic constraints per link type), §Multi-type links.

**Implementation notes:**

- Validation lives here because `quest create`, `quest batch`, and `quest link` all use it. Each caller invokes only what it needs — `create` and `link` call `ValidateSemantic` directly; `batch` calls it per line from phase 4 (`quest.batch.semantic`) after the graph phase has already rejected cycles and depth violations.
- Cycle detection is only for `blocked-by`. Use iterative DFS over the existing dependency graph plus the in-flight edges. Cycle path is a `[]string` for error reporting. Batch's phase 3 (`quest.batch.graph`) also runs cycle detection across all proposed edges at once; both paths use the same DFS helper but emit different error codes (`SemanticDepError.cycle` for `ValidateSemantic`; the batch graph phase emits `cycle` alongside batch-specific `depth_exceeded`).
- Semantic constraints (full table is in the spec):
  - `blocked-by` → target must not be `cancelled`.
  - `retry-of` → target must be `failed`.
  - `caused-by` → source must have `type=bug`.
  - `discovered-from` → source must have `type=bug`.
- Error codes on `SemanticDepError`: `cycle`, `blocked_by_cancelled`, `retry_target_status`, `source_type_required`, `unknown_task_id`. These codes feed both the CLI stderr JSONL and the OTEL events. The batch parse/reference/graph phases emit their own code set (`malformed_json`, `missing_field`, `empty_file`, `duplicate_ref`, `unresolved_ref`, `ambiguous_reference`, `depth_exceeded`, `invalid_tag`, `invalid_link_type`) owned by `internal/batch/`. Some string values (`cycle`, `blocked_by_cancelled`, `retry_target_status`, `source_type_required`, `unknown_task_id`) appear in both sets and describe the same class of failure; the `phase` discriminator on batch errors (which is absent on `SemanticDepError`) disambiguates which code path emitted them. `TestBatchStderrShape` iterates spec §Batch error output; a new `TestValidateSemanticErrorCodes` iterates the `SemanticDepError` set independently.

**Tests:** Layer 1 cycle detection on in-memory graphs; Layer 3 semantic checks against real tasks in the store.

**Done when:** `quest create`, `quest batch` (Task 7.3), and `quest link` (Task 9.1) all share `ValidateSemantic` and pass the full constraint table.

---

### Task 7.3 — `quest batch FILE [--partial-ok]`

**Deliverable:** `internal/batch/` package (the four validation phases) + `internal/command/batch.go` (CLI glue).

**Spec anchors:** `quest-spec.md` §`quest batch` — full section including the error-code table, atomicity semantics, and the example JSONL output format.

**Implementation notes:**

- JSONL reader: one object per non-blank line. Blank lines (whitespace-only) are skipped without affecting line numbers per spec.
- Four phases, each a function that takes the batch and returns a `[]BatchError`. `BatchError` fields: `Line int`, `Phase string`, `Code string`, plus code-specific fields per the spec's "Extra fields" table.
- Cross-phase isolation: a line that failed an earlier phase is excluded from later phases — do not emit derived errors (e.g., "unresolved ref" because the referenced line was malformed). Track a `valid` bitmap across phases.
- Atomic mode (default): any validation error → zero tasks created, exit 2.
- `--partial-ok`: create the subset of lines that passed every phase AND whose references all resolve to created-or-existing tasks. Exit 2 even on partial success (the non-zero exit signals the planner that follow-up is needed). Emit the full ref→id mapping on stdout (JSONL) for created tasks; emit errors on stderr (JSONL).
- **Runtime failures are atomic in both modes** per spec §Batch error handling: `--partial-ok` applies to validation failures only. The creation step runs in a single transaction regardless of mode; if a runtime error occurs mid-insert (constraint violation, lock timeout, internal error), the whole transaction rolls back and no tasks from the batch are created. Exit 7 for lock timeout, 1 for unexpected failures. No partial-success output is produced for runtime failures — only the error is emitted.
- **Stderr JSONL uses the streaming `output.NewJSONLEncoder(stderr).Encode(errObj)` form** introduced in Task 4.3 — batch errors accrue across phases with heterogeneous field sets per error code (`duplicate_ref` carries `first_line`, `cycle` carries `cycle`, `invalid_tag` carries `field`/`value`, etc.), so the slice-typed `EmitJSONL[T]` form does not fit. Stdout's ref→id mapping is uniformly `{"ref": "...", "id": "..."}` and uses the slice form `output.EmitJSONL(stdout, []RefIDPair{...})` (which internally calls the same encoder). Both writers are passed in from the handler descriptor (Task 4.2); never hardcode `os.Stderr`/`os.Stdout` — tests substitute `bytes.Buffer`. Sharing the underlying encoder between the two emission sites prevents quoting / trailing-newline / UTF-8 drift. This is the one place in quest where JSONL is written to stderr (per `OBSERVABILITY.md` §Stderr); every other stderr line is either a slog record or the `quest: <class>: ...` tail.
- Validation prunes failure-dependent lines before creation begins (spec §Batch validation: "A line that fails an earlier phase is excluded from later-phase evaluation"), so the creation step only sees lines whose refs all resolve. **Phase 1 (parse) runs outside the transaction; phases 2–4 (reference, graph, semantic) and the creation pass all run inside a single `s.BeginImmediate(ctx, store.TxBatchCreate)` transaction** (the `tx_kind` enum in `OTEL.md` §4.3 / §5.3 uses `batch_create` specifically for batch-level atomicity). Per the H12 decision, validation that reads the pre-existing graph (parent statuses, `blocked-by` target statuses, cycle detection over batch-internal + existing edges) must hold the write lock from the start to prevent a TOCTOU race: without the lock, a concurrent planner could cancel a `blocked-by` target, close a cycle, or move a parent out of `open` between validation and insert, and the batch would commit a spec-violating graph. Holding the lock for the full batch increases lock-wait for other writers proportional to batch size (the §5.2 histogram caps at ~500 tasks, so worst-case hundreds of ms), but quest's serialized-writes contract (exit 7 + caller retry) already accepts that regime. If the transaction fails for a runtime reason (constraint violation, lock timeout, etc.), abort the whole tx and report cleanly. One `quest.store.tx{tx_kind=batch_create}` span represents the full unit of work (validation + creation).
- Phase 4 (`quest.batch.semantic`) calls `deps.ValidateSemantic` (Task 7.2) per line. Phase 3 (`quest.batch.graph`) owns cycle detection across all proposed edges at once plus `depth_exceeded` — do not fold those into `ValidateSemantic`. Some codes appear in both the batch phase set and the `SemanticDepError` set (`cycle`, `blocked_by_cancelled`, `retry_target_status`, `source_type_required`, `unknown_task_id`) because they describe the same class of failure; the `phase` discriminator on batch errors distinguishes which code path emitted them.
- Any `tags` field in a batch line goes through the same validator as `quest create --tag` (spec §Tags > Validation: `^[a-z0-9][a-z0-9-]*$`, 1–32 chars after lowercasing). Validation failures emit the `invalid_tag` code added in spec §Batch error output (phase `semantic`), with `field` (e.g., `tags[2]` — zero-indexed position within the line's tags array) and `value` (the offending tag). `TestBatchStderrShape` (Task 13.1) iterates the full error-code table including `invalid_tag`.
- `ref` resolution: `ref` maps to an internal batch label; during creation, resolve to the actual generated ID. External `id` references are looked up in the store.
- `parent` accepts three input shapes per spec §Batch file format: a bare string (shorthand for `{"ref": "<s>"}`), `{"ref": "<s>"}`, or `{"id": "<s>"}`. The same object shape already applies to `dependencies[]` entries — factor the parse+validation into one `parseRef(raw json.RawMessage, allowBareString bool) (RefTarget, error)` helper used by both call sites. **The bare-string shorthand is spec-scoped to `parent` only**: `parseRef` is called with `allowBareString=true` from the parent path and `allowBareString=false` from the dependencies path. A bare string in a `dependencies[]` entry returns the `ambiguous_reference` parse error (phase 1) with `field` set to `dependencies[n]` — the spec requires dependencies to use the disambiguated `{"ref": "..."}` or `{"id": "..."}` object form. `RefTarget` is a small struct with exactly-one-of `Ref string` / `ID string`; having both keys set or neither set returns the same `ambiguous_reference` error with `field` set to `parent` or `dependencies[n]`. Unresolved `ref` → `unresolved_ref`; unknown `id` → `unknown_task_id` (both phase 2, unchanged from spec). Layer 2 contract test: a batch line with `"dependencies": ["task-1"]` emits `ambiguous_reference` on stderr with `field: "dependencies[0]"`.
- **Dependencies `type` enum check at phase `semantic`.** Each `dependencies[n]` entry carries a `type` string that must be one of `blocked-by`, `caused-by`, `discovered-from`, `retry-of`. Validate this inside phase 4 (`quest.batch.semantic`) — the same phase as `invalid_tag`, which is the nearest precedent for an enum-rejection check. Unknown values emit the `invalid_link_type` code (now in spec §Batch error output per the amendment) with `field` set to `dependencies[n].type` (zero-indexed array position) and `value` set to the offending string. Without this check, a typo (`"blockd-by"`) would reach the creation step and surface as a SQL constraint violation with a less helpful message. `TestBatchStderrShape` (Task 13.1) includes `invalid_link_type` in its coverage.
- **Text-mode output.** `--format text` renders the ref→id map as a two-column table (`REF`, `ID`) via `output.Table` (Task 4.3). Text mode remains not-a-contract per spec §Output & Error Conventions; agents consume `--format json` (the default).
- **Telemetry wiring** (Phase 12): at handler exit (after the creation tx commits or aborts), call `telemetry.RecordBatchOutcome(ctx, linesTotal, linesBlank, partialOK, createdCount, errorsCount)` per Task 12.11. The recorder records `dept.quest.batch.size{outcome}` and the §4.3 `quest.batch.*` attribute set on the command span. Per-line content recorders (`RecordContentTitle` etc.) fire from inside the creation loop for each successfully created task, gated on `CaptureContentEnabled()`.

**Tests:** Layer 2 contract (every error code appears on stderr with the documented fields), Layer 3 (atomic vs partial, cross-phase isolation, the cycle/depth edge cases), Layer 1 for the pure parse/reference phases.

Fixtures in `internal/batch/testdata/` — see `TESTING.md` §Test Fixtures for the naming convention.

**Done when:** every error code in the spec's error-code table is covered by a fixture and a test.

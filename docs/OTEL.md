# OpenTelemetry Design -- Quest

**Status:** Design spec
**Date:** 2026-04-16
**Semconv version:** v1.40.0 (pinned to the framework standard in `~/dev/grove/otel-guide.md`)
**Upstream:** `~/dev/grove/otel-guide.md`
**Sibling:** `~/dev/lore/docs/OTEL.md`
**Pending discussion:** `otel-discuss.md` (review items H6 and M5 are not yet reflected here)

---

## 1. Overview

This document specifies the OpenTelemetry instrumentation design for quest. It translates the framework-level OTEL design recommendations into concrete decisions for quest's single-binary CLI architecture, SQLite-backed storage, and agent-first command surface.

Quest has exactly one instrumentation target:

- **`quest`** -- short-lived CLI. Initializes the OTEL SDK at startup with a short shutdown timeout. Creates a root span per invocation, extracts `TRACEPARENT` from the environment, executes the command against a local SQLite database, flushes on exit.

Unlike lore, quest is not a two-process system. There is no daemon, no wire protocol, and no trans-process propagation inside the tool. `TRACEPARENT` comes in from the enclosing agent session and is consumed by a single process. A `questd` daemon is noted as deferred in the quest spec; if it ships, section 18 describes how this design extends.

---

## 2. Applicable Principles

These are the framework-level principles from the design recommendations, restated as they apply to quest. They are not repeated in full -- see the upstream doc for rationale.

1. **Telemetry never affects application behavior.** OTEL failures are logged and discarded. The SQLite database (tasks, history, dependencies, tags) is the primary output. No codepath gates on OTEL succeeding.
2. **Zero cost when disabled.** When `OTEL_EXPORTER_OTLP_ENDPOINT` is not set, install explicit no-op providers at startup. No goroutines, no allocations, no periodic flushes. Because quest is invoked from agent prompts thousands of times per session, this is non-negotiable.
3. **Opt-in by default.** Telemetry activates when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Content capture (task titles, descriptions, debrief/handoff/note/reason text) requires `OTEL_GENAI_CAPTURE_CONTENT=true`.
4. **Quest owns its own spans.** Quest creates spans for quest operations only. It does not instrument vigil, lore, or rite internals. The span hierarchy joins via `TRACEPARENT` propagated by vigil.
5. **The host process wires the SDK.** `cmd/quest/main.go` (or equivalent CLI entrypoint) initializes the TracerProvider, MeterProvider, and LoggerProvider. Internal packages import only OTEL API packages.
6. **Correlation by trace context.** The existing `AGENT_TASK` and `AGENT_SESSION` env vars continue to work independently of OTEL. `TRACEPARENT` provides a parallel correlation path via trace IDs. Either can work without the other.
7. **Follow OTel GenAI semantic conventions.** Use the `gen_ai.*` attribute namespace. Pin to semconv v1.40.0 (matches the framework guide; updated from the earlier draft's v1.26.0, which predated the CNCF GenAI attribute reorganization).

---

## 3. Signal Strategy

All three OTEL signals from day one, per the framework recommendation.

| Signal      | Purpose in quest                                                                                                                                                                                                                                                               |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **Traces** | Decompose command lifecycle: CLI invocation -> argument parsing -> role gating -> validation -> structural transaction -> SQLite writes -> history append. Reconstruct the full execution tree when quest is called within a vigil agent session, joined with lore and rite spans under the same root. |
| **Metrics** | Aggregate dashboards: operation counts by command and exit code, latency histograms, status-transition volumes, task-creation rates by tier and role, batch size distribution, write-lock wait time. Answer "what is the p95 of `quest batch`" without trace queries.          |
| **Logs**    | High-cardinality event records: exact validation error messages, cycle paths, which children blocked a parent close, which specific task IDs were affected by `quest move`. Emitted via the OTEL log bridge from slog.                                                         |

### 3.1 Log bridge

Quest should use `log/slog` for all diagnostic output (recommended even independent of OTEL -- matches lore and the grove conventions). Install the `otelslog` bridge handler that forwards slog records to the OTEL LoggerProvider so logs, spans, and metrics correlate via trace and span IDs.

The bridge handler composes with the existing stderr handler via a fan-out handler, not by replacing it. Stderr remains the human-readable diagnostic channel; OTEL gets structured records. This mirrors lore section 3.1.

Quest does not accept a log file path -- all non-protocol diagnostics go to stderr. Writing to stderr does not participate in the fan-out; the fan-out is the slog `Handler` layer, which produces records fed into both the stderr and OTEL handlers in parallel.

Slog call sites in quest should use the `*Context(ctx, ...)` variants (`InfoContext`, `ErrorContext`, etc.) so the bridge captures trace and span IDs. Lifecycle calls before a context exists (e.g., SDK init errors during `main`) use the non-context variants and accept that they will not carry trace correlation; this is correct -- no trace exists yet.

### 3.2 Log event inventory

Without an explicit inventory the logs signal devolves into "whatever the developer wrote." The following table is the design-time contract for which events quest emits, at what severity, and with which attributes. Developers adding new log call sites match an existing category or propose a new one.

| Category          | Event name (`slog.Info/Warn/Error` message)        | Severity | Required attributes                                                         | When                                                               |
| ----------------- | -------------------------------------------------- | -------- | --------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| **lifecycle**     | `quest command start`                              | DEBUG    | `command`, `agent.role`, `dept.task.id`                                     | Root command span created                                          |
| **lifecycle**     | `quest command complete`                           | DEBUG    | `command`, `exit_code`, `duration_ms`                                       | Command handler returns                                            |
| **decision**      | `role gate denied`                                 | INFO     | `command`, `agent.role`, `required=elevated`                                | Worker-role command denied                                         |
| **decision**      | `batch mode fallthrough`                           | INFO     | `partial_ok`, `errors_total`, `created_total`                               | `quest batch --partial-ok` proceeded with partial errors           |
| **validation**    | `batch validation error`                           | WARN     | `phase`, `line`, `code`, `field`, `ref?`                                    | Per-line batch error; also emitted as span event (§4.4)            |
| **validation**    | `dep cycle detected`                               | WARN     | `cycle.path`, `cycle.length`                                                | `quest link --blocked-by` or batch graph phase finds a cycle        |
| **conflict**      | `precondition failed`                              | INFO     | `command`, `task.id`, `precondition`, `blocked_by_ids?`                     | Exit-code-5 paths (parent non-terminal children, etc.)             |
| **transient**     | `write lock timeout`                               | WARN     | `tx_kind`, `wait_ms`                                                        | Exit-code-7 path; also drives `dept.quest.store.lock_timeouts`     |
| **error**         | `internal error`                                   | ERROR    | `command`, `task.id?`, `err`                                                | Unexpected failures (exit code 1); `err` truncated to 256 chars     |
| **migration**     | `schema migration applied`                         | INFO     | `schema.from`, `schema.to`, `applied_count`                                 | Startup schema upgrade; one record per invocation (at most)         |
| **otel-internal** | `otel internal error`                              | WARN     | `err`                                                                       | OTEL `SetErrorHandler` routing (§7.4)                              |

**Severity policy.** `DEBUG` lifecycle entries are normally filtered out at the slog `Handler` level; they exist for local development (slog call sites use them unconditionally but a level filter suppresses them when `DEPT_LOG_LEVEL=info` or higher). `INFO` carries decision and conflict events. `WARN` carries validation/transient failures the user can act on. `ERROR` is reserved for bugs.

**Bridge filtering.** The `otelslog` bridge handler in §3.1 receives the full slog record stream. A level filter sits in the fan-out handler so stderr (for humans) and the OTEL bridge (for backends) can be level-gated independently: stderr defaults to `WARN`, the OTEL bridge defaults to `INFO`, both are overridable via `DEPT_LOG_LEVEL` and a separate `DEPT_LOG_OTEL_LEVEL` variable. Level filtering is the only supported mechanism for reducing log volume -- quest does not ship a per-category on/off switch.

**Content protection.** No log event emits task titles, descriptions, handoff text, debrief text, notes, reasons, or metadata. Content flows only through span events under `OTEL_GENAI_CAPTURE_CONTENT` (§4.5). Log attributes carry IDs, counts, enums, and error classes -- nothing else.

---

## 4. Span Architecture

### 4.1 Span hierarchy

When quest is called from within a vigil agent session, the span tree looks like:

```
vigil.session (root -- set by vigil, propagated via TRACEPARENT)
  └── execute_tool quest.{command} (root span in the quest process)
        ├── quest.db.migrate (only when schema_version lags; emitted at most once per invocation)
        ├── quest.role.gate (when an elevated command runs)
        ├── quest.validate (command-specific validation, e.g., batch or dependency)
        │     ├── quest.batch.parse
        │     ├── quest.batch.reference
        │     ├── quest.batch.graph
        │     └── quest.batch.semantic
        ├── quest.store.tx (structural transactions -- BEGIN IMMEDIATE)
        │     └── quest.store.{operation} (SQL operations inside the transaction)
        └── quest.store.{operation} (SQL operations outside a structural transaction)
```

When quest is called outside a vigil session (human running `quest list` from a shell, no `TRACEPARENT`), the CLI creates a root trace:

```
execute_tool quest.{command} (root)
  └── ... (same sub-span structure as above)
```

### 4.2 Span inventory

**Command-level spans (one per CLI invocation, root in the quest process):**

| Span Name                       | Span Kind | When                                         |
| ------------------------------- | --------- | -------------------------------------------- |
| `execute_tool quest.init`       | INTERNAL  | `quest init`                                 |
| `execute_tool quest.show`       | INTERNAL  | `quest show`                                 |
| `execute_tool quest.accept`     | INTERNAL  | `quest accept`                               |
| `execute_tool quest.update`     | INTERNAL  | `quest update`                               |
| `execute_tool quest.complete`   | INTERNAL  | `quest complete`                             |
| `execute_tool quest.fail`       | INTERNAL  | `quest fail`                                 |
| `execute_tool quest.create`     | INTERNAL  | `quest create`                               |
| `execute_tool quest.batch`      | INTERNAL  | `quest batch`                                |
| `execute_tool quest.cancel`     | INTERNAL  | `quest cancel`                               |
| `execute_tool quest.reset`      | INTERNAL  | `quest reset`                                |
| `execute_tool quest.move`       | INTERNAL  | `quest move`                                 |
| `execute_tool quest.link`       | INTERNAL  | `quest link`                                 |
| `execute_tool quest.unlink`     | INTERNAL  | `quest unlink`                               |
| `execute_tool quest.tag`        | INTERNAL  | `quest tag`                                  |
| `execute_tool quest.untag`      | INTERNAL  | `quest untag`                                |
| `execute_tool quest.deps`       | INTERNAL  | `quest deps`                                 |
| `execute_tool quest.list`       | INTERNAL  | `quest list`                                 |
| `execute_tool quest.graph`      | INTERNAL  | `quest graph`                                |
| `execute_tool quest.export`     | INTERNAL  | `quest export`                               |
| `execute_tool quest.version`    | INTERNAL  | `quest version` -- **suppressed** (see §4.2 Excluded)    |

**Span kind rationale.** Command spans use INTERNAL, matching the GenAI convention for `execute_tool`. Unlike lore, quest has no CLI-daemon hop, so there is no CLIENT/SERVER split to model. If a `questd` daemon is introduced later (see section 18), the CLI span becomes CLIENT and the daemon-side handler span becomes SERVER, identical to lore's pattern.

**Child spans (created inside command spans):**

| Span Name                    | Span Kind | When                                                                                             |
| ---------------------------- | --------- | ------------------------------------------------------------------------------------------------ |
| `quest.db.migrate`           | INTERNAL  | Startup, when the binary's supported version exceeds the stored `schema_version`                 |
| `quest.role.gate`            | INTERNAL  | Any elevated command, after `AGENT_ROLE` is checked against `elevated_roles`                     |
| `quest.validate`             | INTERNAL  | Commands with multi-phase validation (`create`, `link`, `move`, etc.) -- parent of sub-phase spans |
| `quest.batch.parse`          | INTERNAL  | `quest batch` phase 1 (JSONL parse, required-field presence)                                     |
| `quest.batch.reference`      | INTERNAL  | `quest batch` phase 2 (ref uniqueness, resolution to refs/IDs)                                   |
| `quest.batch.graph`          | INTERNAL  | `quest batch` phase 3 (cycle detection, depth check)                                             |
| `quest.batch.semantic`       | INTERNAL  | `quest batch` phase 4 (type constraints on dependency links)                                     |
| `quest.store.tx`             | INTERNAL  | Every `BEGIN IMMEDIATE` transaction (structural ops; see spec Storage section). Lock wait, tx body, and outcome are the daemon-upgrade signal |
| `quest.store.traverse`       | INTERNAL  | Graph traversal over `tasks` + `dependencies` (`quest graph`, `quest deps`, `--ready` filter). Cost scales with graph size |
| `quest.store.rename_subgraph`| INTERNAL  | ID rewrite over a subgraph (`quest move`). Non-uniform cost -- N row-rewrites × M dep-edge rewrites                            |

**Store-level DML as span events, not spans.** Uniform single-row store operations (`insert_task`, `update_task`, `append_history`, `insert_dep`, `delete_dep`, `update_tags`, `select_task`, `list_tasks`, `export.write`) are recorded as events on the parent command span rather than their own child spans. Their cost is reliably a small fraction of the parent command span's duration; promoting each to a span produces noise without diagnostic signal, and the CLI is invoked at a rate where per-row spans compound into real volume pressure on the collector.

Each event uses the static name `quest.store.op` with attributes:

| Attribute       | Source                                                                                     |
| --------------- | ------------------------------------------------------------------------------------------ |
| `db.system`     | Static `"sqlite"`                                                                          |
| `db.operation`  | `insert_task`, `update_task`, `append_history`, `insert_dep`, `delete_dep`, `update_tags`, `select_task`, `list_tasks`, `export.write` |
| `db.target`     | Logical target: `tasks`, `history`, `dependencies`, `tags`, `export` (file)                |
| `rows_affected` | Int -- rows changed (for writes) or returned (for reads); omitted for reads without a natural count |
| `duration_ms`   | Float -- operation duration in milliseconds                                                |

Errors from a collapsed operation are still recorded via the three-step pattern (§4.4) on the parent command span -- `span.RecordError` + `span.SetStatus` + metric counter -- with `db.operation` added to the error event. The `quest.store.tx`, `quest.store.traverse`, and `quest.store.rename_subgraph` spans retain their own error recording per §8.4.

**Span name convention.** Span names are static. Dynamic values (task IDs, refs, query parameters, error messages) go in attributes or span events, never in names. `{command}` in `execute_tool quest.{command}` is a bounded enum from the command inventory, not a user-supplied string.

**Child-span naming carve-out (intentional deviation from guide §3.2).** The guide prescribes `{operation} {target}` for all spans (e.g., `execute_tool quest.create`). Root command spans follow this. Child spans (`quest.store.tx`, `quest.batch.parse`, `quest.role.gate`, `quest.db.migrate`, etc.) do **not** follow `{operation} {target}` and do **not** carry `gen_ai.tool.name`. They are internal subsystem spans, not tool-level operations, and forcing them into the GenAI pattern would misrepresent them (they are not `execute_tool` calls). This carve-out is deliberate and consistent across the quest child-span inventory; do not mix naming styles. Root spans remain the canonical GenAI-convention touchpoint for cross-tool queries.

**Depth vs. noise tradeoff.** Store-level spans are created only when the operation has independent diagnostic value -- a slow `quest create` is usually a slow `quest.store.tx`, not a slow argument parse. Pure in-memory work (argument parsing, `--format` rendering, JSONL serialization) is not instrumented with spans; its cost is rolled into the parent command span's duration.

**Excluded from span instrumentation.**

- Argument parsing and flag validation -- captured as latency on the command span; errors here surface via span status and the `dept.quest.errors` counter.
- `quest version` -- **no span, no metric**. Pure-informational command with no DB access and no diagnostic value. Metric counters do not increment for `version`. Apply the same rule to any future `--help`-style informational flags handled before dispatch.
- Pure in-memory JSON/text rendering -- not instrumented.
- Workspace discovery (walking up from CWD looking for `.quest/`) -- not instrumented. Fast, hit-rate near 100%, no diagnostic value.

### 4.3 Required span attributes

**All command spans (`execute_tool quest.{command}`):**

| Attribute               | Source                                      | Notes                                                                 |
| ----------------------- | ------------------------------------------- | --------------------------------------------------------------------- |
| `gen_ai.tool.name`      | `"quest." + command`                        | e.g., `quest.create`, `quest.batch`. Dashboards parse the command from this attribute |
| `gen_ai.operation.name` | `"execute_tool"`                            | Per GenAI conventions                                                 |
| `gen_ai.agent.name`     | `AGENT_ROLE` env, via `roleOrUnset`         | Empty env value is recorded as the literal string `"unset"`. Same convention on spans and metrics -- see §8.6 |
| `dept.task.id`          | `AGENT_TASK` env                            | Task correlation tag from vigil. Empty for planners acting across tasks |
| `dept.session.id`       | `AGENT_SESSION` env                         | Session correlation tag from vigil. Required framework attribute (`otel-guide.md` §3.3) -- mirrors `dept.task.id` but for sessions |
| `quest.role.elevated`   | Bool -- whether the command is elevated     | Recorded after role gating                                            |

> `quest.command` (previously listed here) has been removed. It duplicated `gen_ai.tool.name` and violated guide principle 1.7 (do not invent proprietary attributes for operations the conventions already cover). Dashboards derive the command from `gen_ai.tool.name` by stripping the `quest.` prefix.

**Task-affecting commands (`show`, `accept`, `update`, `complete`, `fail`, `cancel`, `reset`, `move`, `link`, `unlink`, `tag`, `untag`, `create`, `deps`) -- additional:**

| Attribute             | Source                             | Notes                                                                                      |
| --------------------- | ---------------------------------- | ------------------------------------------------------------------------------------------ |
| `quest.task.id`       | Target task ID                     | May differ from `dept.task.id` -- a planner running `quest cancel proj-01.3` affects a task other than their `AGENT_TASK` |
| `quest.task.status.from` | Status before the transition    | Present on status-changing commands (`accept`, `complete`, `fail`, `cancel`, `reset`)      |
| `quest.task.status.to`   | Status after the transition     | Present on status-changing commands                                                        |
| `quest.task.tier`     | Tier (T0-T6)                       | Present on `create` and on commands that inspect the task (e.g., `show`)                   |
| `quest.task.role`     | Assigned role (or empty)           | Present on `create`                                                                        |
| `quest.task.type`     | `task` or `bug`                    | Present on `create` and task-affecting commands                                            |
| `quest.task.parent`   | Parent task ID                     | Present on `create --parent`, `move`, and commands that touch parent-child structure        |

**`quest batch` -- additional:**

| Attribute                 | Source                                        |
| ------------------------- | --------------------------------------------- |
| `quest.batch.lines_total` | Total lines read (excluding blank lines)      |
| `quest.batch.lines_blank` | Blank lines skipped                           |
| `quest.batch.partial_ok`  | Whether `--partial-ok` was set                |
| `quest.batch.created`     | Count of tasks actually created               |
| `quest.batch.errors`      | Count of error records written to stderr      |

**`quest link` / `quest unlink` -- additional:**

| Attribute         | Source                                                          |
| ----------------- | --------------------------------------------------------------- |
| `quest.link.type` | Relationship type: `blocked-by`, `caused-by`, etc.              |
| `quest.link.target` | Target task ID                                                |

**`quest move` -- additional:**

| Attribute                 | Source                                                        |
| ------------------------- | ------------------------------------------------------------- |
| `quest.move.old_id`       | Task's ID before the move                                     |
| `quest.move.new_id`       | Task's ID after the move                                      |
| `quest.move.subgraph_size`| Count of tasks renamed (self + descendants)                   |
| `quest.move.dep_updates`  | Count of dependency edges rewritten to the new IDs            |

**`quest cancel -r` -- additional:**

| Attribute                    | Source                                                        |
| ---------------------------- | ------------------------------------------------------------- |
| `quest.cancel.recursive`     | Bool -- whether `-r` was set                                  |
| `quest.cancel.cancelled_count` | Count of tasks transitioned to `cancelled`                  |
| `quest.cancel.skipped_count`   | Count of descendants skipped (already terminal)             |

**`quest list` / `quest graph` / `quest deps` -- additional:**

| Attribute                       | Applies to          | Source                                                      |
| ------------------------------- | ------------------- | ----------------------------------------------------------- |
| `quest.query.filter.status`     | `list`              | Status filter, comma-joined (low cardinality since bounded) |
| `quest.query.filter.role`       | `list`              | Role filter                                                 |
| `quest.query.filter.tier`       | `list`              | Tier filter                                                 |
| `quest.query.filter.type`       | `list`              | Type filter                                                 |
| `quest.query.ready`             | `list`              | Bool -- whether `--ready` was set                           |
| `quest.query.result_count`      | `list`, `deps`      | Number of results returned                                  |
| `quest.graph.node_count`        | `graph`             | Nodes in the resulting graph                                |
| `quest.graph.edge_count`        | `graph`             | Edges in the resulting graph                                |
| `quest.graph.external_count`    | `graph`             | External leaves in the graph                                |

Tag filters (`--tag go,auth`) and parent filters (`--parent proj-a1`) are not recorded as span attributes because tag and parent IDs are user-supplied strings with unbounded cardinality. They appear in logs (via the slog bridge) when validation or execution emits a record; traces do not need the exact filter to be useful.

**`quest.store.tx` and other DB-touching spans -- additional:**

| Attribute              | Source                                                                                       |
| ---------------------- | -------------------------------------------------------------------------------------------- |
| `db.system`            | Static `"sqlite"`. Cached with `sync.Once`; emitted on every span that issues SQL (see §9.2 of the guide) |
| `quest.tx.kind`        | Structural transaction type: `accept_parent`, `create_child`, `complete_parent`, `move`, `cancel_recursive` |
| `quest.tx.lock_wait_ms`| Duration (ms) between `BEGIN IMMEDIATE` issue and lock acquisition                           |
| `quest.tx.rows_affected` | Total rows affected inside the transaction                                                 |
| `quest.tx.outcome`     | `committed`, `rolled_back_precondition`, or `rolled_back_error`. Set at the close of the transaction |

**`quest.db.migrate` -- additional:**

| Attribute                | Source                                |
| ------------------------ | ------------------------------------- |
| `quest.schema.from`      | Version before migration              |
| `quest.schema.to`        | Version after migration               |
| `quest.schema.applied_count` | Number of migration steps applied |

### 4.4 Error recording

Every error follows the three-step pattern:

```go
span.RecordError(err)
span.SetStatus(codes.Error, truncate(err.Error(), 256))
errCounter.Add(ctx, 1, metric.WithAttributes(cmdAttr, exitCodeAttr))
```

Quest's error taxonomy is exit code-centric (the quest spec defines codes 1-7). The span records both the exit code and a stable error class derived from it:

| Exit code | `quest.error.class`  | `quest.error.retryable` |
| --------- | -------------------- | ----------------------- |
| 1         | `general_failure`    | false                   |
| 2         | `usage_error`        | false                   |
| 3         | `not_found`          | false                   |
| 4         | `permission_denied`  | false                   |
| 5         | `conflict`           | false                   |
| 6         | `role_denied`        | false                   |
| 7         | `transient_failure`  | true                    |

`quest.error.class` is a bounded vocabulary safe for metric dimensions. The raw exit code is recorded as a span attribute (`quest.exit_code`) but NOT as a metric dimension -- the class carries the same information with better dashboard ergonomics.

On exit code 7 specifically, the span also records:

| Attribute                  | Source                                           |
| -------------------------- | ------------------------------------------------ |
| `quest.lock.wait_limit_ms` | 5000 (the busy_timeout ceiling)                  |
| `quest.lock.wait_actual_ms`| Actual wait time observed before giving up       |

Exit code 7 is the threshold signal that drives the deferred `questd` daemon decision (see quest spec, Storage section). Making it first-class in both spans and metrics gives operators the data they need to decide when contention has exceeded SQLite's single-writer ceiling.

**Validation errors (batch).** Each error in a `quest batch` stderr stream is also emitted as a span event on the `quest.batch.{phase}` child span where it occurred:

```
event: quest.batch.error
attrs: line=5, code=missing_field, field=title
```

Errors are also counted in the `dept.quest.batch.errors` counter with a `phase` dimension.

### 4.5 Content capture

Gated on `OTEL_GENAI_CAPTURE_CONTENT=true`, checked once at initialization inside `setup.go` under `sync.Once` and stored as a package-level plain `bool`. No atomic is needed because the flag is written once before any command handler runs and is only read thereafter -- matches guide §14.2.

When enabled, content goes in span events (not span attributes):

| Event Name                    | Data                                                      | Max Length |
| ----------------------------- | --------------------------------------------------------- | ---------- |
| `quest.content.title`         | Task title                                                | 256 chars  |
| `quest.content.description`   | Task description                                          | 1024 chars |
| `quest.content.context`       | Task context                                              | 1024 chars |
| `quest.content.acceptance_criteria` | Acceptance criteria                                 | 512 chars  |
| `quest.content.note`          | Progress note text                                        | 512 chars  |
| `quest.content.debrief`       | Debrief text                                              | 1024 chars |
| `quest.content.handoff`       | Handoff text                                              | 1024 chars |
| `quest.content.reason`        | Reason text (for `cancel`, `reset`)                       | 512 chars  |

**Event naming prefix.** Events use `quest.*`, not `dept.quest.*`. Spans and metrics are cross-tool signals (dashboards query across `dept.*`), so they carry the framework prefix; events are always scoped to a specific span and do not need the disambiguating prefix. This is intentional and consistent with OTEL idiom -- event names sit in the span's local namespace. Keep all event names under `quest.*`; do not mix prefixes across the event inventory.

When disabled, these events are not emitted and no string allocation occurs. Check the flag before truncation, not after.

**Never in telemetry (under any flag):**

- `metadata` field contents -- planner-defined free-form JSON, unbounded shape and content
- History `content` fields beyond the live task (e.g., old handoff text stored in `handoff_set` history entries) -- these can accumulate unbounded and have no debugging value outside the audit log itself
- Full raw SQL statements -- internal implementation detail; the span name and structured attributes already convey the operation

### 4.6 Attribute value truncation

| Data Type                                  | Quest max | Guide baseline (§3.6) | Note                                                   |
| ------------------------------------------ | --------- | --------------------- | ------------------------------------------------------ |
| Error messages                             | 256 chars | 256 chars             | Matches guide                                          |
| ID prefix validation error detail          | 256 chars | --                    | Quest-specific; aligned with error-message ceiling     |
| Task title (content capture on)            | 256 chars | --                    | Titles are short by spec; 256 is generous              |
| Descriptions/context/debrief/handoff (content capture on) | 1024 chars | 1024 chars (memory content) | Matches guide. Worker briefs over 1024 chars should live in task context files, not span events |
| Notes, reasons, acceptance criteria (content capture on)  | 512 chars  | 512 chars (search queries) | Matches guide                                    |
| File paths (`@file` inputs on the command line) | 256 chars | --            | Path only, never content                                |
| Raw SQL statements                         | not emitted | 300 chars           | Quest deliberately does not capture raw SQL (§4.5 "Never in telemetry"); span name + structured attributes already convey the operation |

Use UTF-8-safe truncation per the framework recommendation. A single `truncate(s, max)` helper in `internal/telemetry/truncate.go` walks backward from the cut point to avoid splitting multi-byte runes; a companion `truncateIDList(ids []string, max int)` cuts at comma boundaries for variable-length ID lists emitted on error events (see §13.3).

---

## 5. Metric Architecture

### 5.1 Instruments

| Metric Name                       | Type            | Unit          | Dimensions                        | Description                                                                                   |
| --------------------------------- | --------------- | ------------- | --------------------------------- | --------------------------------------------------------------------------------------------- |
| `dept.quest.operations`           | Counter         | `{operation}` | `command`, `status`               | Total CLI invocations by command and outcome (`ok`, `error`)                                  |
| `dept.quest.operation.duration`   | Histogram       | `ms`          | `command`                         | Latency distribution per command                                                              |
| `dept.quest.errors`               | Counter         | `{operation}` | `command`, `error_class`          | Errored invocations by command and error class (from exit code mapping in 4.4)                |
| `dept.quest.tasks.created`        | Counter         | `{task}`      | `tier`, `role`, `type`            | Tasks created (via `quest create` or `quest batch`)                                           |
| `dept.quest.tasks.completed`      | Counter         | `{task}`      | `tier`, `role`, `outcome`         | Tasks reaching a terminal state (`complete`, `failed`, `cancelled`)                           |
| `dept.quest.status_transitions`   | Counter         | `{task}`      | `from`, `to`                      | All status transitions -- primary retrospective input for lifecycle analysis                  |
| `dept.quest.links`                | Counter         | `{link}`      | `link_type`, `action`             | Dependency link additions/removals (`action` = `added` or `removed`)                          |
| `dept.quest.batch.size`           | Histogram       | `{task}`      | `outcome`                         | Tasks-per-batch distribution (`outcome` = `ok`, `partial`, `rejected`)                        |
| `dept.quest.batch.errors`         | Counter         | `{event}`     | `phase`, `code`                   | Batch validation errors by phase and error code                                               |
| `dept.quest.store.tx.duration`    | Histogram       | `ms`          | `tx_kind`                         | `BEGIN IMMEDIATE` transaction duration by kind                                                |
| `dept.quest.store.tx.lock_wait`   | Histogram       | `ms`          | `tx_kind`                         | Time spent waiting for the SQLite write lock                                                  |
| `dept.quest.store.lock_timeouts`  | Counter         | `{operation}` | `tx_kind`                         | Operations that exited with code 7 (the daemon-upgrade threshold metric)                      |
| `dept.quest.query.result_count`   | Histogram       | `{task}`      | `command`                         | Result counts for `list`, `graph`, `deps` -- sizing signal                                    |
| `dept.quest.graph.traversal_nodes`| Histogram       | `{task}`      | `command`                         | Nodes visited during graph traversal (separate from returned count)                            |
| `dept.quest.schema.migrations`    | Counter         | `{event}`     | `from_version`, `to_version`      | Schema migrations applied                                                                     |

**Unit strings.** UCUM-style `{thing}` units name the entity being counted, not the cause. Counters standardize on `{operation}` (for operation-counters), `{task}` (for task-entity counters), `{link}` (for link-edge counters), and `{event}` (for generic event counters with no stable entity). Durations use `ms`.

**Relationship between `dept.quest.operations` and `dept.quest.errors`:** `dept.quest.errors` is a strict superset of `dept.quest.operations{status=error}` broken down by `error_class`. Use `operations` for overall error rate; use `errors` for per-class breakdown. **Do not sum them** -- they double-count.

### 5.2 Histogram bucket boundaries

- **Command duration (`dept.quest.operation.duration`):** 1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000 ms. Most quest commands complete well under 100ms; bursts into the upper buckets indicate contention or large batches.
- **Transaction duration (`dept.quest.store.tx.duration`):** 1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000 ms. Same shape as commands; structural transactions are the expected slow path.
- **Lock wait (`dept.quest.store.tx.lock_wait`):** 0, 1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000 ms. The 5000ms bucket is the exit-code-7 ceiling; values at or above it are transient failures.
- **Batch size (`dept.quest.batch.size`):** 1, 2, 5, 10, 25, 50, 100, 250, 500. Most deliverables decompose into tens of tasks; very large batches warrant scrutiny.
- **Result counts (`dept.quest.query.result_count`, `dept.quest.graph.traversal_nodes`):** 0, 1, 5, 10, 25, 50, 100, 250, 500, 1000.

### 5.3 Attribute cardinality rules

**Safe for metrics (low cardinality):**

- `command` -- bounded set from the command inventory in 4.2
- `status` -- `ok`, `error`
- `error_class` -- bounded vocabulary from 4.4
- `tier` -- bounded set `T0`-`T6`
- `role` -- bounded by role templates in the department (empirically small; a cap of ~20 distinct values is the implicit ceiling)
- `type` -- `task`, `bug`
- `outcome` -- `complete`, `failed`, `cancelled` (for tasks); `ok`, `partial`, `rejected` (for batches)
- `from`, `to` (status transitions) -- bounded enum product
- `link_type` -- `blocked-by`, `caused-by`, `discovered-from`, `retry-of`
- `action` -- `added`, `removed`
- `tx_kind` -- bounded: `accept_parent`, `create_child`, `complete_parent`, `move`, `cancel_recursive`
- `phase` -- `parse`, `reference`, `graph`, `semantic`
- `code` -- bounded batch error codes (from spec §Batch error output)
- `from_version`, `to_version` -- integers; growth is bounded by release count

**Never on metrics (high cardinality):**

- Task IDs, session IDs, request IDs
- Task titles, descriptions, debriefs, handoffs, reasons, notes
- Tag values, parent IDs, project prefixes
- User-supplied filter values (except the bounded enum filters in the `quest.query.filter.*` attributes above -- those are span-only)

`role` sits at the boundary. If a department's role templates proliferate unusually, `role` can grow to dozens of values. This is acceptable for metric dimensions at the scale quest operates (one-deliverable-per-workspace); if a deployment discovers role cardinality is an issue in their backend, they can drop the dimension via OTEL Collector view configuration.

**Combined cardinality ceiling.** `dept.quest.tasks.created` (`tier × role × type`) produces at most `7 × |roles| × 2` series; `dept.quest.tasks.completed` (`tier × role × outcome`) produces `7 × |roles| × 3`. For typical department sizes (|roles| < 20), combined cardinality on these two counters stays under 500 series -- comfortable for Prometheus and close to the recommended ceiling for managed backends that cap at 1K-10K series per metric. Operators whose backend has a lower ceiling should drop the `role` dimension via Collector view config rather than removing the whole metric.

---

## 6. Trace Context Propagation

### 6.1 Propagation chain

Quest is a leaf in the framework's propagation chain -- it receives context, consumes it, and emits spans underneath it. There is no downstream tool to propagate to.

```
vigil (sets TRACEPARENT in agent session env)
  ▼
agent session (TRACEPARENT in environment)
  ▼
quest CLI (extracts TRACEPARENT from env, creates child span)
  ▼
OTLP collector / backend
```

### 6.2 Extract from environment

Same pattern as lore section 6.2. Done once at CLI startup, before any span is created.

**Prerequisite:** `telemetry.Setup()` must register the global W3C composite text map propagator before `ExtractTraceFromEnv` is called (see §7.1). Without it, `otel.GetTextMapPropagator().Extract(...)` silently becomes a no-op against the default no-op propagator, and `TRACEPARENT` is never consumed -- a silent, easy-to-miss failure mode.

```go
func extractTraceFromEnv(ctx context.Context) context.Context {
    traceparent := os.Getenv("TRACEPARENT")
    if traceparent == "" {
        return ctx
    }
    carrier := propagation.MapCarrier{"traceparent": traceparent}
    if ts := os.Getenv("TRACESTATE"); ts != "" {
        carrier["tracestate"] = ts
    }
    return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
```

### 6.3 When trace context is absent

When `TRACEPARENT` is not set (human running `quest list` from a shell, CI-driven imports, development), quest creates a root span if OTEL is configured, or all span calls are no-ops if it is not. No special handling needed.

### 6.4 Session correlation beyond trace context

`AGENT_SESSION` is read from the environment and recorded as `dept.session.id` on the root span. This is correlation metadata, not trace context -- it gives retrospectives a join key to vigil's session records even when `TRACEPARENT` is absent. It is redundant with trace context when both are present; keeping both is cheap and protects against configurations where one signal is missing.

### 6.5 No inbound-from-daemon case

Quest has no daemon today. There is no CLI-to-daemon hop, no wire protocol envelope for trace propagation, and no transport-specific extraction middleware. If `questd` is introduced (see section 18), it will follow lore's pattern: first-class `trace_parent`, `agent_role`, `agent_task`, and `agent_session` fields on the request envelope.

---

## 7. SDK Initialization

### 7.1 CLI entrypoint

The CLI initializes the SDK early in `main`, after parsing globals but before dispatching to a command handler. Command handlers **must not** call `os.Exit` -- they return an integer exit code to `main`. `main` lets deferred shutdown run before passing the returned code to `os.Exit`. See §7.6 for the rationale.

```go
func main() {
    os.Exit(run())
}

func run() int {
    // ... workspace discovery, arg parsing, slog setup ...

    ctx := context.Background()
    otelShutdown, _ := telemetry.Setup(ctx, telemetry.Config{
        ServiceName:    "quest-cli",
        ServiceVersion: buildinfo.Version,
    })
    defer func() {
        if otelShutdown != nil {
            shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            _ = otelShutdown(shutdownCtx)
        }
    }()

    ctx = telemetry.ExtractTraceFromEnv(ctx)

    return dispatch(ctx /* returns int exit code */)
}
```

- **Service name:** `quest-cli`. Matches guide §8.2, preserves dashboard continuity when the deferred `questd` daemon (section 18) ships as `quest-daemon`. A single binary today is not a reason to diverge from the framework naming; renaming later breaks historical dashboards.
- **Default exporter protocol:** HTTP (`http/protobuf`) -- works through more proxies, better for short-lived processes. If `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` is set, quest logs a single warning at startup (via slog) that gRPC is not linked into the CLI and falls back to HTTP. See §7.5.
- **Span processor:** `BatchSpanProcessor` with `sdktrace.WithBatchTimeout(1 * time.Second)`. For sub-second CLI runs, the shutdown flush captures all pending spans in a single batch export. This replaces an earlier draft choice of `SimpleSpanProcessor`, which the framework guide forbids in production (§7.4 "Never use SimpleSpanProcessor in production") -- per-span synchronous export puts full exporter round-trip latency on the critical path of every `span.End()`.
- **Log processor:** `BatchLogRecordProcessor` with `WithExportInterval(1 * time.Second)`, matching the span processor. `quest batch` in particular can emit many validation-error log records in one invocation; the batch processor handles bursts cleanly and the shutdown flush captures anything pending.
- **Metric reader:** `PeriodicReader` (SDK default for metric export). No special tuning -- the shutdown flush exports the final window.
- **Sampler:** `ParentBased(TraceIDRatioBased(1.0))` -- sample everything, respect upstream sampling decisions. **Operator guidance:** when trace volume exceeds collector capacity, reduce the ratio via `OTEL_TRACES_SAMPLER_ARG` (for example, `0.1` for 10%). Prefer tail-based sampling at the OTEL Collector -- it lets the Collector keep all error traces and slow traces regardless of the ratio.
- **Shutdown timeout:** 5 seconds. Consistent with lore CLI.
- **Init errors:** silently discarded (logged via slog at debug level). Quest still works without telemetry.
- **Global propagator and error handler:** Registered inside `telemetry.Setup` -- see §7.3 and §7.4.

### 7.2 Conditional initialization

When `OTEL_EXPORTER_OTLP_ENDPOINT` is not set, `telemetry.Setup` installs explicit no-op providers and returns a no-op shutdown function. Zero overhead.

When `OTEL_SDK_DISABLED=true`, same behavior.

### 7.3 Global propagator registration

`telemetry.Setup` registers the global W3C composite propagator **before returning**. Skipping this is the most common quiet OTEL failure: `Extract`/`Inject` silently become no-ops and traces from vigil never join to quest's root spans. Registered even when exporters fail to construct, so that pure in-process propagation still works.

```go
otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
    propagation.TraceContext{}, // W3C traceparent + tracestate
    propagation.Baggage{},      // W3C baggage (reserved for future use)
))
```

### 7.4 OTEL error handler

`telemetry.Setup` registers an `otel.SetErrorHandler` that routes OTEL internal errors (export failures, metric-recording errors, resource detection failures) to `slog` at warn level. The default error handler prints to stderr, which quest treats as a protocol channel (JSONL batch errors, structured messages); without this override a failed OTLP export could interleave noise into a parent agent's stderr parser. Registration is unconditional and cheap.

```go
otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
    slog.Warn("otel internal error", "err", err)
}))
```

### 7.5 OTLP protocol honoring

The Go SDK honors `OTEL_EXPORTER_OTLP_ENDPOINT` automatically for whichever exporter is linked. Quest links the HTTP exporter only (to keep the CLI dependency footprint small). At startup, quest reads `OTEL_EXPORTER_OTLP_PROTOCOL` and, if it is set to `grpc` or `grpc/protobuf`, emits a single slog warn record noting that the CLI supports HTTP only and is falling back. The process does not fail -- telemetry continues over HTTP. If the deferred `questd` daemon (section 18) ships, the daemon will link gRPC exporters and honor the env var directly without this warning path.

### 7.6 No `os.Exit` in command handlers

Command handlers **must not** call `os.Exit` directly. Go's `os.Exit` skips deferred functions -- any deferred shutdown in `main` is bypassed and all buffered telemetry for that invocation is silently dropped. With `BatchSpanProcessor`, spans that have ended but not yet exported live in the batch queue until the periodic flush or `Shutdown`; `os.Exit` destroys them without a flush.

**Invariant:** handlers return `int` (the exit code) to `main`. `main` wraps `run()` so that deferred shutdown completes before `os.Exit(code)` is called. Lint or a code-review norm enforces "no `os.Exit` outside `main`." Add a checklist item to §19.

Early-exit paths at the `main` level (argument-parsing errors before dispatch) return before any span is created and do not produce telemetry beyond the shutdown flush of already-emitted spans. This is correct -- argument parsing is not interesting enough to warrant its own span, and exit code 2 (usage error) has no span data worth collecting.

### 7.7 Shutdown ordering

Quest has no long-lived connections to drain. Shutdown order:

1. Command handler returns an int exit code (root span's `defer span.End()` fires).
2. Any deferred child spans end.
3. `otelShutdown(ctx)` -- flushes the batch span processor, the batch log processor, and the periodic metric reader; closes exporters.
4. `main` calls `os.Exit(exitCode)`.

Because `BatchSpanProcessor` buffers spans, `Shutdown()` is where the final OTLP export happens for a sub-second CLI. A 5-second timeout bounds the exporter call.

### 7.8 Partial-init cleanup

When initializing multiple providers, later failures must not leak already-initialized providers' goroutines (the `PeriodicReader` in the metric provider, the batch processor in the tracer provider). Init order is tracer → meter → logger; on failure at step N, call `Shutdown()` on steps 1 to N-1 before returning.

```go
tp, err := buildTracerProvider(ctx, res)
if err != nil {
    return nil, fmt.Errorf("tracer provider: %w", err)
}
mp, err := buildMeterProvider(ctx, res)
if err != nil {
    _ = tp.Shutdown(ctx)
    return nil, fmt.Errorf("meter provider: %w", err)
}
lp, err := buildLoggerProvider(ctx, res)
if err != nil {
    _ = mp.Shutdown(ctx)
    _ = tp.Shutdown(ctx)
    return nil, fmt.Errorf("logger provider: %w", err)
}
```

Matches guide §6.4. Without this, a failed log exporter leaves the metric periodic-reader goroutine running for the rest of the (short) process lifetime -- harmless today, but a silent leak that grows problematic if the CLI is invoked in a long-lived agent session shell.

### 7.9 Idempotent shutdown

Shutdown is called once from `defer` in `main`. Quest does not install signal handlers that could trigger a second shutdown; CLIs receiving SIGINT typically exit via the Go runtime default, and any in-flight spans at that moment are lost -- acceptable for the CLI case. The framework-recommended mutex-guarded pattern is overkill here but trivially cheap; use it regardless for consistency with lore's `telemetry.Setup` contract.

---

## 8. Instrumentation Architecture

### 8.1 Package structure

```
internal/telemetry/
  setup.go          SDK init, shutdown, resource config, conditional no-op
  context.go        Context keys (e.g., for command name, session ID carry)
  propagation.go    TRACEPARENT extraction from env
  recorder.go       RecordX functions for command-level events
  command.go        CommandSpan helper (creates the root `execute_tool quest.*` span)
  store.go          InstrumentedStore decorator (wraps the store interface)
  validation.go     Span helpers for batch validation phases
  truncate.go       UTF-8-safe value truncation
  fanout.go         Fan-out slog handler for the OTEL log bridge
```

All business logic calls `telemetry.RecordX()` or uses the decorator/middleware wrappers. No OTEL API imports in `cmd/quest/`, `internal/cli/`, `internal/store/` (or wherever the DB layer lives), or the command-handler packages.

### 8.2 Command-level span helper

Every command handler wraps its work in a root span via a single helper. This keeps span creation consistent and centralizes attribute naming. `AGENT_ROLE`, `AGENT_TASK`, and `AGENT_SESSION` are read once at init and cached in a package-level struct; `CommandSpan` reads from the cache rather than calling `os.Getenv` on every invocation:

```go
// internal/telemetry/env.go -- populated once by telemetry.Setup
var envOnce sync.Once
var envAttrs struct {
    agentRole    string // post-roleOrUnset
    agentTask    string
    agentSession string
}

func loadEnv() {
    envOnce.Do(func() {
        envAttrs.agentRole = roleOrUnset(os.Getenv("AGENT_ROLE"))
        envAttrs.agentTask = os.Getenv("AGENT_TASK")
        envAttrs.agentSession = os.Getenv("AGENT_SESSION")
    })
}

// internal/telemetry/command.go
func CommandSpan(ctx context.Context, command string, elevated bool) (context.Context, trace.Span) {
    loadEnv()
    return tracer.Start(ctx, "execute_tool quest."+command,
        trace.WithAttributes(
            attribute.String("gen_ai.tool.name", "quest."+command),
            attribute.String("gen_ai.operation.name", "execute_tool"),
            attribute.String("gen_ai.agent.name", envAttrs.agentRole),
            attribute.String("dept.task.id", envAttrs.agentTask),
            attribute.String("dept.session.id", envAttrs.agentSession),
            attribute.Bool("quest.role.elevated", elevated),
        ),
    )
}
```

Command handlers call this once and defer `span.End()`. Quest-specific attributes (task ID, tier, transition, etc.) are added post-creation via typed recorder functions in `recorder.go`.

**No manual no-op short-circuit.** An earlier draft had `CommandSpan` check `Enabled()` and return a non-recording span without calling `tracer.Start`. That has been removed -- it risked breaking trace propagation when a valid parent context was present and fragmented the "disabled" path into two logics. Rely on the no-op providers installed by §7.2: `tracer.Start` on a no-op provider is already near-zero cost (no exporter round-trip, no allocation beyond the returned interface), and it preserves the parent context correctly.

**Optional wrapper: `WrapCommand`.** `CommandSpan` is the primitive; most handlers pair it with the §4.4 three-step error pattern at every exit. To avoid duplicating that boilerplate, `internal/telemetry/command.go` also exposes a middleware-style wrapper:

```go
// Runs fn inside a CommandSpan. On a non-nil returned error, records it via the
// three-step pattern (RecordError + SetStatus + dept.quest.errors counter) using
// errors.ExitCode(err) to classify. Always calls span.End().
func WrapCommand(ctx context.Context, command string, elevated bool,
    fn func(ctx context.Context) error) error
```

Handlers that want the sugar write `return telemetry.WrapCommand(ctx, "accept", elevated, func(ctx context.Context) error { ... })`. Handlers that need finer-grained span control (multiple error sites with different exit codes, or span attributes set mid-handler) use `CommandSpan` directly. Both paths emit the same spans and metrics; `WrapCommand` is a call-site convenience, not a second instrumentation point.

### 8.3 Instrumented store decorator

Primary instrumentation point for storage. Wraps the quest store interface (whatever name it has -- `Store`, `TaskStore`, etc.) with an identically-shaped implementation that records telemetry around each operation:

```go
type InstrumentedStore struct {
    inner Store
    // package-level tracer and meter; no per-instance fields
}

func WrapStore(s Store) Store {
    if !Enabled() {
        return s
    }
    return &InstrumentedStore{inner: s}
}
```

The decorator's store methods fall into two emission shapes:

1. **Spans** for operations with distinctive cost profiles -- `quest.store.tx` (§8.4), `quest.store.traverse`, and `quest.store.rename_subgraph`. These wrap the underlying call in `tracer.Start`/`span.End` and record the attributes listed in §4.3.
2. **Span events** for uniform single-row DML -- `insert_task`, `update_task`, `append_history`, `insert_dep`, `delete_dep`, `update_tags`, `select_task`, `list_tasks`, and `export.write`. The decorator measures duration, then calls `span.AddEvent("quest.store.op", ...)` on the current span (typically the parent command span). No child span is created.

A small helper centralizes the event path so every call site emits the same attribute set:

```go
func (s *InstrumentedStore) recordStoreEvent(ctx context.Context, op, target string, rows int64, start time.Time, err error) {
    dur := float64(time.Since(start).Microseconds()) / 1000.0
    span := trace.SpanFromContext(ctx)
    attrs := []attribute.KeyValue{
        attribute.String("db.system", "sqlite"),
        attribute.String("db.operation", op),
        attribute.String("db.target", target),
        attribute.Float64("duration_ms", dur),
    }
    if rows >= 0 {
        attrs = append(attrs, attribute.Int64("rows_affected", rows))
    }
    span.AddEvent("quest.store.op", trace.WithAttributes(attrs...))
    if err != nil {
        span.RecordError(err, trace.WithAttributes(attribute.String("db.operation", op)))
        span.SetStatus(codes.Error, truncate(err.Error(), 256))
    }
}
```

Duration uses `float64(time.Since(start).Microseconds()) / 1000.0` (matches lore convention -- microseconds-to-ms at float64 precision gives useful sub-millisecond resolution).

**Why events, not spans, for the uniform set.** Span volume compounds with CLI invocation rate. A single `quest create --parent --blocked-by foo,bar` can drive three or four store writes; each as its own span adds parent-child bookkeeping and collector bytes without giving the operator a diagnostic signal they would actually query on. The three retained spans (`tx`, `traverse`, `rename_subgraph`) are the ones where a duration handle is genuinely wanted. See also §15 on lock-contention observability -- the signal lives on `quest.store.tx`, not on the individual inserts inside it.

`--no-track` is not a quest concept (see 9.2); the decorator does not carry a bypass flag.

### 8.4 Structural-transaction span

`BEGIN IMMEDIATE` transactions get their own span -- they are the primary contention point and the signal for the daemon-upgrade decision:

```go
func StoreTx(ctx context.Context, kind string) (context.Context, EndTxFunc) {
    start := time.Now()
    ctx, span := tracer.Start(ctx, "quest.store.tx",
        trace.WithAttributes(
            attribute.String("db.system", "sqlite"),
            attribute.String("quest.tx.kind", kind),
        ),
    )

    // inside: lock acquisition completes; record lock wait
    lockAcquired := time.Now()
    lockWaitMs := float64(lockAcquired.Sub(start).Microseconds()) / 1000.0
    span.SetAttributes(attribute.Float64("quest.tx.lock_wait_ms", lockWaitMs))
    lockWaitHist.Record(ctx, lockWaitMs, metric.WithAttributes(attribute.String("tx_kind", kind)))

    return ctx, func(outcome TxOutcome, rowsAffected int64, err error) {
        dur := float64(time.Since(start).Microseconds()) / 1000.0
        span.SetAttributes(
            attribute.Int64("quest.tx.rows_affected", rowsAffected),
            attribute.String("quest.tx.outcome", outcome.String()), // "committed", "rolled_back_precondition", "rolled_back_error"
        )
        txDurHist.Record(ctx, dur, metric.WithAttributes(attribute.String("tx_kind", kind)))
        if err != nil {
            if isLockTimeout(err) {
                lockTimeoutCtr.Add(ctx, 1, metric.WithAttributes(attribute.String("tx_kind", kind)))
            }
            span.RecordError(err)
            span.SetStatus(codes.Error, truncate(err.Error(), 256))
        }
        span.End()
    }
}
```

The `TxOutcome` enum distinguishes normal commit from the two rollback modes: `rolled_back_precondition` (a preconditional check inside the transaction failed, e.g., parent not in `open`) and `rolled_back_error` (an unexpected error). Precondition rollbacks are the "happy unhappy path" -- they surface exit code 5 (conflict) but are not bugs; separating them from `rolled_back_error` keeps dashboards clean.

The store helper is the only place that distinguishes "lock wait" from "transaction body time" -- both are useful. Lock wait specifically drives the daemon-upgrade signal.

### 8.5 Batch validation spans

`quest batch` creates a parent `quest.validate` span with one child per phase (`quest.batch.parse`, `quest.batch.reference`, `quest.batch.graph`, `quest.batch.semantic`). Each phase span records its own error event stream via span events and also increments `dept.quest.batch.errors` with the phase dimension.

When validation fails before creation (atomic mode, default), the handler skips the creation step entirely; the command span's status reflects exit code 2 and the `quest.batch.created` attribute is `0`. In `--partial-ok` mode, creation proceeds for surviving tasks; the command span still records a non-ok status because errors were reported.

### 8.6 Recording functions for post-operation enrichment

For attributes known only after an operation completes (task ID of a newly created task, rows affected, transition counts), use explicit recorder functions:

```go
func RecordTaskCreated(ctx context.Context, taskID, tier, role, taskType string) {
    span := trace.SpanFromContext(ctx)
    span.SetAttributes(
        attribute.String("quest.task.id", taskID),
        attribute.String("quest.task.tier", tier),
        attribute.String("quest.task.role", role),
        attribute.String("quest.task.type", taskType),
    )
    tasksCreatedCtr.Add(ctx, 1, metric.WithAttributes(
        attribute.String("tier", tier),
        attribute.String("role", roleOrUnset(role)),
        attribute.String("type", taskType),
    ))
}

func RecordStatusTransition(ctx context.Context, taskID, from, to string) {
    span := trace.SpanFromContext(ctx)
    span.SetAttributes(
        attribute.String("quest.task.id", taskID),
        attribute.String("quest.task.status.from", from),
        attribute.String("quest.task.status.to", to),
    )
    statusTransitionsCtr.Add(ctx, 1, metric.WithAttributes(
        attribute.String("from", from),
        attribute.String("to", to),
    ))
}
```

Similar recorders for `RecordLinkAdded`, `RecordLinkRemoved`, `RecordBatchOutcome`, `RecordMoveOutcome`, `RecordCancelOutcome`, `RecordQueryResult`, `RecordGraphResult`, `RecordMigration`, and content-capture helpers.

**`roleOrUnset(role)`** maps empty role to the literal string `"unset"`. The same helper is applied on **both** span attributes (`gen_ai.agent.name`) and metric dimensions (`role`, `agent.role`), so cross-signal correlation queries ("traces where `gen_ai.agent.name=coder` joined to metrics where `role=coder`") work without special-casing the no-role case. An earlier draft used raw `""` on spans and `"unset"` on metrics; that mismatch has been removed.

### 8.7 Role-gating span

When an elevated command runs, a `quest.role.gate` child span records the gate check. This gives retrospectives a clean signal for "how often did worker-role commands attempt elevated operations" independent of the terminal status of the parent command:

```go
ctx, span := tracer.Start(ctx, "quest.role.gate",
    trace.WithAttributes(
        attribute.String("quest.role.required", "elevated"),
        attribute.String("quest.role.actual", agentRole),
        attribute.Bool("quest.role.allowed", allowed),
    ),
)
span.End()
```

If the gate denies the command, the parent span records `exit_code=6` and error class `role_denied`, and the handler returns without executing the command body. The gate span is the fine-grained record; the command span is the outcome record.

### 8.8 Schema migration span

At startup, before any command runs, if the binary's supported schema version exceeds the stored version, quest runs migrations inside a single transaction. This emits `quest.db.migrate` as a root-level span:

```go
ctx, span := tracer.Start(ctx, "quest.db.migrate",
    trace.WithAttributes(
        attribute.Int("quest.schema.from", currentVersion),
        attribute.Int("quest.schema.to", targetVersion),
    ),
)
defer span.End()
```

On success, `quest.schema.applied_count` is added. On failure (migration error), the span records the error and the binary exits with code 1; no command span is created.

Because migrations happen before the command span, the `quest.db.migrate` span is a sibling of the command span in the trace -- both children of the upstream `TRACEPARENT` context (if present) or both root spans in their own traces (if absent).

---

## 9. Privacy, Security, and the Absence of `--no-track`

### 9.1 Content capture (privacy)

See section 4.5. Default-off. Gated on `OTEL_GENAI_CAPTURE_CONTENT=true`. Content goes in span events, never span attributes, to keep attribute indexes clean.

### 9.2 No `--no-track` equivalent

Lore exposes `--no-track` to let the curator read memories without self-inflating the ops table. Quest has no analogous mechanism and does not need one:

- Quest's `history` table is append-only by design. Reads do not write to it. A curator reading quest tasks (`quest show`, `quest list`, `quest graph`) produces no history entries regardless of OTEL state.
- Retrospective tooling reading quest does produce OTEL spans and metric increments. If dashboard distortion from retrospective traffic becomes a concern, operators can filter by `gen_ai.agent.name = curator` (or whichever role the retrospective runs under) at the Collector or backend.
- Introducing `--no-track` to quest would add a flag with no internal privacy guarantee to protect -- the flag exists in lore because the ops table is a persistent, agent-visible record. Quest has no equivalent persistent tracking layer distinct from its core data model.

This is an explicit decision. Revisit only if retrospective-traffic distortion becomes a concrete operational problem.

### 9.3 Never in telemetry

Under any flag combination:

- `metadata` field contents (planner-defined free-form JSON)
- Old handoff text stored in `handoff_set` history entries (unbounded accumulation)
- Raw SQL statements
- Full `@file` input contents (only the file path is ever a span attribute, and only truncated to 256 chars)

---

## 10. Module Dependencies

### 10.1 API vs SDK boundary

| Package                    | Imports OTEL API | Imports OTEL SDK   | Notes                                                         |
| -------------------------- | ---------------- | ------------------ | ------------------------------------------------------------- |
| `internal/telemetry/`      | Yes              | `setup.go` only    | Only `setup.go` imports SDK + exporters                       |
| `internal/cli/`            | No               | No                 | Calls `telemetry.Setup` and `telemetry.ExtractTraceFromEnv`   |
| `internal/command/` (handlers) | No           | No                 | Calls `telemetry.CommandSpan`, `telemetry.RecordX`            |
| `internal/store/` (DB)     | No               | No                 | Instrumented via `InstrumentedStore` wrapper                   |
| `internal/validate/`       | No               | No                 | Calls `telemetry.ValidationPhase` wrapper                     |
| `cmd/quest/`               | No               | No                 | `cli.Execute` handles SDK setup                               |

Package names are illustrative -- actual layout matches the quest repository structure.

### 10.2 Go module dependencies

```
# API packages
go.opentelemetry.io/otel
go.opentelemetry.io/otel/trace
go.opentelemetry.io/otel/metric
go.opentelemetry.io/otel/attribute
go.opentelemetry.io/otel/codes
go.opentelemetry.io/otel/propagation

# SDK packages (setup.go only)
go.opentelemetry.io/otel/sdk/trace
go.opentelemetry.io/otel/sdk/metric
go.opentelemetry.io/otel/sdk/resource
go.opentelemetry.io/otel/sdk/log
go.opentelemetry.io/otel/semconv/v1.40.0

# Exporters (setup.go only)
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp
go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp

# slog bridge
go.opentelemetry.io/contrib/bridges/otelslog
```

Quest ships HTTP exporters only -- gRPC exporters are omitted because the CLI does not need them and pulling them in doubles the dependency footprint. If `questd` is introduced, the daemon adds gRPC exporters in its own `cmd/questd/` package without affecting the CLI.

### 10.3 Tracer and meter caching

Package-level variables in `internal/telemetry`:

```go
var tracer = otel.Tracer("dept.quest")
var meter  = otel.Meter("dept.quest")
```

All instrumentation -- `CommandSpan`, `InstrumentedStore`, recorders -- uses these. Matches lore section 11.3.

**Scope vs. span/metric naming:**

- Instrumentation scope: `dept.quest`
- Span names: `execute_tool quest.*` (root), `quest.*` (children) -- GenAI convention for root, quest-specific for internals
- Metric names: `dept.quest.*` -- consistent prefix for cross-tool dashboards

---

## 11. Resource Definition

```go
resource.NewWithAttributes(
    semconv.SchemaURL,
    semconv.ServiceName("quest-cli"),
    semconv.ServiceVersion(buildinfo.Version),
)
```

No `service.instance.id` -- each CLI invocation is ephemeral. `deployment.environment` is **not** set in code (an earlier draft hardcoded it via `attribute.String` per guide §6.2 pattern; quest drops that because the value is operator-specific). Operators supply it via `OTEL_RESOURCE_ATTRIBUTES=deployment.environment=production`, which the Go SDK parses and merges into the resource automatically. `department.name` and other environment-specific attributes follow the same pattern.

---

## 12. Environment Variables

### 12.1 Standard OTEL (read by Go SDK automatically)

| Variable                       | Quest behavior                                                                                                                 |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| `OTEL_EXPORTER_OTLP_ENDPOINT`  | Activates telemetry when set                                                                                                   |
| `OTEL_EXPORTER_OTLP_PROTOCOL`  | Default: `http/protobuf`. Quest links the HTTP exporter only; if set to `grpc`, quest logs a single warn record at startup and falls back to HTTP. See §7.5 |
| `OTEL_SDK_DISABLED`            | Kill switch -- explicit no-op providers                                                                                        |
| `OTEL_TRACES_SAMPLER`          | Default: `parentbased_traceidratio`                                                                                            |
| `OTEL_TRACES_SAMPLER_ARG`      | Default: `1.0`. Operators reduce (e.g., `0.1`) when collector volume warrants; tail-based sampling at the Collector is preferred for keeping error/slow traces |
| `OTEL_SERVICE_NAME`            | Overrides hardcoded `quest-cli`                                                                                                |
| `OTEL_RESOURCE_ATTRIBUTES`     | Additional resource attributes (e.g., `deployment.environment=production`)                                                     |
| `OTEL_BSP_MAX_QUEUE_SIZE` / `OTEL_BSP_SCHEDULE_DELAY` / `OTEL_BSP_EXPORT_TIMEOUT` | Honored by the SDK. Quest's `WithBatchTimeout(1s)` default can be overridden this way |

### 12.2 GenAI convention

| Variable                         | Quest behavior                                                                                           |
| -------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `OTEL_GENAI_CAPTURE_CONTENT`     | When `true`, adds content events (titles, descriptions, debrief/handoff/note/reason text) to spans       |
| `OTEL_SEMCONV_STABILITY_OPT_IN`  | Not applicable. Quest emits conventions as pinned at semconv v1.40.0 and does not offer a per-invocation toggle. If semconv v2.0 ships before quest v1, revisit |

### 12.3 Framework-specific (set by vigil, read by quest CLI)

| Variable       | Purpose                                                                  |
| -------------- | ------------------------------------------------------------------------ |
| `AGENT_ROLE`   | Agent identity -- role gating, `gen_ai.agent.name`, history              |
| `AGENT_TASK`   | Default target for worker commands, `dept.task.id` span attribute, history |
| `AGENT_SESSION`| Session correlation -- `dept.session.id` span attribute, history         |
| `TRACEPARENT`  | W3C trace context -- parent span for the root command span               |
| `TRACESTATE`   | W3C trace state -- carried forward for sampling decisions only           |

These are hardcoded framework conventions, not per-tool configuration. Every tool in the framework reads the same env vars the same way.

---

## 13. Error Handling Patterns

### 13.1 Three-step pattern

Every error path in a command handler or store method follows:

```go
span.RecordError(err)
span.SetStatus(codes.Error, truncate(err.Error(), 256))
errorsCtr.Add(ctx, 1, metric.WithAttributes(
    attribute.String("command", command),
    attribute.String("error_class", classifyExitCode(exitCode)),
))
```

### 13.2 Exit-code class mapping

See the table in 4.4. `classifyExitCode` is a small helper in `internal/telemetry/recorder.go` that maps the integer exit code to the string class. Exit codes not in the table (should not occur; indicates a bug) map to `general_failure`.

### 13.3 Attributes on error events for cross-row failures

Commands that fail with exit code 5 due to non-terminal children (`quest accept`, `quest complete` on a parent) record the blocking children as span event attributes:

```
event: quest.precondition.failed
attrs: quest.blocked_by_count=3, quest.blocked_by_ids="proj-a1.1,proj-a1.2,proj-a1.3"
```

The ID list is recorded on the event only -- it is high-cardinality for metrics but diagnostically valuable in the trace. Use `truncateIDList(ids []string, maxLen int) string` from `internal/telemetry/truncate.go`, which cuts at comma boundaries (never mid-ID) and appends a `,...(+N more)` suffix so the reader knows the list was truncated. Cap at 256 chars (the error-message limit). The comma-aware variant is required -- a mid-ID cut produces an invalid ID fragment that a trace reader could misinterpret as a real task.

### 13.4 Cycle-detection failures

`quest link --blocked-by` and `quest batch` in the graph phase can detect cycles. The cycle path is recorded as a span event:

```
event: quest.dep.cycle_detected
attrs: quest.cycle.path="proj-a1->proj-a2->proj-a1", quest.cycle.length=3
```

Cycle path strings are truncated to 512 chars. The metric (`dept.quest.batch.errors` with code=`cycle`) fires independently.

---

## 14. Testing

### 14.1 No-panic verification

Every `RecordX` function and every `InstrumentedStore` method is tested against no-op providers:

```go
func TestInstrumentedStoreWithNoopProvider(t *testing.T) {
    store := telemetry.WrapStore(testutil.NewFakeStore())
    _, err := store.GetTask(ctx, "proj-01")
    if err != nil { t.Fatal(err) }
}
```

### 14.2 Span assertion tests

In-memory exporter with `SimpleSpanProcessor`:

```go
exporter := tracetest.NewInMemoryExporter()
tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
prev := otel.GetTracerProvider()
otel.SetTracerProvider(tp)
t.Cleanup(func() { otel.SetTracerProvider(prev) })

// ... run instrumented command ...

spans := exporter.GetSpans()
// Assert: root span name, attributes, parent-child, error status
```

Tests always save and restore the previous provider via `t.Cleanup`.

### 14.3 What to assert

- Root span name is `execute_tool quest.{command}` with the correct command
- Required attributes present: `gen_ai.tool.name`, `gen_ai.operation.name`, `gen_ai.agent.name`, `dept.task.id`, `dept.session.id`, `quest.role.elevated`
- `quest.store.tx` spans appear for structural commands (`accept` on parent, `create --parent`, `complete` on parent, `move`, `cancel -r`)
- Exit code 5 on parent complete with non-terminal children produces `quest.error.class=conflict` and a `quest.precondition.failed` event
- Exit code 7 produces `quest.error.class=transient_failure` and increments `dept.quest.store.lock_timeouts`
- Batch validation errors produce per-phase events and `dept.quest.batch.errors` counters
- Content events absent when `OTEL_GENAI_CAPTURE_CONTENT` unset
- Content events present when `OTEL_GENAI_CAPTURE_CONTENT=true`, with truncation to the lengths in 4.5
- Status-transition counters increment with correct `from`/`to` dimensions

### 14.4 Instrument creation validation

A test that creates all instruments against a real SDK provider and asserts no errors, catching invalid metric names at test time. Matches lore section 15.3.

### 14.5 Benchmark

Targets (measured as delta between instrumented and uninstrumented paths):

- Under 5 additional allocations per command when OTEL is enabled.
- Zero additional allocations when OTEL is disabled (no-op provider path).

A `BenchmarkCreateTaskWithTracing` and `BenchmarkBatchWithTracing` cover the two hottest paths. `quest create` is the most-invoked write command; `quest batch` is the largest single transaction.

### 14.6 Exit-code-to-class coverage

A table-driven test that asserts every exit code in the spec (1-7) maps to the correct `error_class` and that the span's status code and attributes match. Catches regressions when the spec's exit-code table changes.

---

## 15. Lock-Contention Observability (Daemon Upgrade Signal)

The quest spec designates exit code 7 (transient failure -- write lock unavailable after 5s) as the threshold signal for the deferred `questd` daemon. OTEL instrumentation makes this signal first-class:

- **Counter:** `dept.quest.store.lock_timeouts` increments exactly when quest exits with code 7.
- **Histogram:** `dept.quest.store.tx.lock_wait` records the lock-wait duration of every structural transaction, whether or not it succeeded.
- **Alerting query:** "rate of `dept.quest.store.lock_timeouts` > threshold over last hour, or p95 of `dept.quest.store.tx.lock_wait` > 2000ms."
- **Trace query:** find traces where `quest.store.tx` spans carry `quest.tx.lock_wait_ms > 1000` -- identify which commands are the hot waiters.

Operators use these to decide when SQLite's single-writer ceiling is saturated and the `questd` daemon is warranted. Without the instrumentation, the only signal is user-visible exit-code-7 failures, which is too late.

---

## 16. Implementation Sequence

Recommended order, each step independently testable:

1. **`internal/telemetry/setup.go`** -- SDK init, conditional no-op, shutdown, fan-out slog handler. `truncate.go` -- truncation helper. `propagation.go` -- `TRACEPARENT` extraction. Wire into the CLI entrypoint. At this point, quest has OTEL initialized but emits nothing.
2. **slog bridge** -- Install `otelslog` fan-out handler in `setup.go`. Migrate slog call sites to `*Context` variants so subsequent spans benefit from trace-correlated logs.
3. **`CommandSpan` helper + wire into every command handler** -- Every command gets its root span with the required attributes. Verify spans appear in the collector.
4. **Role-gating span** (`quest.role.gate`) -- Add around the gate check in the dispatcher.
5. **`InstrumentedStore` decorator** -- Start with the most common operations (`GetTask`, `InsertTask`, `UpdateTask`, `AppendHistory`). Wire in at the point where the store is constructed. Verify child spans appear under the command span.
6. **`StoreTx` helper** -- Instrument `BEGIN IMMEDIATE` transactions with `quest.store.tx` spans and lock-wait recording. Wire into every structural-transaction call site (accept-parent, create-child, complete-parent, move, cancel-recursive).
7. **Metrics** -- Add `dept.quest.operations`, `dept.quest.errors`, `dept.quest.operation.duration`, `dept.quest.store.tx.duration`, `dept.quest.store.tx.lock_wait`, `dept.quest.store.lock_timeouts`. Verify via backend queries.
8. **Status-transition metric** -- Add `RecordStatusTransition` to every handler that changes status. Verifies the simplest retrospective query works end-to-end.
9. **`quest batch` spans** -- Add `quest.validate` + per-phase spans, `dept.quest.batch.size`, `dept.quest.batch.errors`, and batch-specific recorders.
10. **`quest graph`, `quest list`, `quest deps`** -- Add graph/query attributes and `dept.quest.query.result_count`, `dept.quest.graph.traversal_nodes`.
11. **`quest move`, `quest cancel -r`** -- Add subgraph-size and rows-affected attributes and metrics.
12. **Content capture** -- Add content events to command handlers for titles, descriptions, debrief, handoff, notes, reasons. Gate on `OTEL_GENAI_CAPTURE_CONTENT`.
13. **Schema migration span** -- Add `quest.db.migrate` at startup, before dispatch.
14. **Test coverage** -- No-panic tests, span assertion tests, instrument-creation validation, exit-code-class coverage, benchmarks.

---

## 17. Dependency Impact

Adding OTEL brings a dependency footprint increase similar to lore's:

- ~10 new direct dependencies (API, SDK, HTTP exporters, bridge, semconv -- no gRPC exporters)
- ~20+ transitive dependencies (protobuf, net/http2, etc.)

Quest ships HTTP-only exporters (no gRPC), which trims the dependency count relative to lore. The API/SDK split keeps internal packages on the lightweight API packages; only `setup.go` pulls the heavy exporter transitive closure.

---

## 18. Forward Compatibility: `questd` Daemon

The quest spec (Storage section) describes a deferred `questd` daemon for when concurrent write contention exceeds SQLite's single-writer ceiling. If it ships, this OTEL design extends as follows without breaking existing deployments:

- **Two service names:** `quest-cli` (unchanged from v1) and `quest-daemon` (daemon). Same pattern as lore's `lore-cli` and `lore-daemon`. Using `quest-cli` from v1 means no service-name migration is needed when the split lands.
- **Span kinds split:** On the daemon-ship release, the CLI's `execute_tool quest.{command}` span kind changes from INTERNAL to CLIENT, and the daemon adds a matching SERVER span as its child. Pre-release INTERNAL spans remain as-is in historical data; the change is forward-only and dashboards that query by span name (not kind) continue to work unchanged. Store-layer spans (`quest.store.*`) move to the daemon in the same release.
- **Wire protocol envelope:** If questd uses a JSON request envelope (matching lore), add first-class `agent_role`, `agent_task`, `agent_session`, and `trace_parent` fields. The CLI populates them from env; the daemon extracts trace context and the identity attributes. See lore section 17 for the pattern.
- **Exporter split:** Daemon uses gRPC exporters + BatchSpanProcessor; CLI continues on HTTP + BatchSpanProcessor (with `WithBatchTimeout(1s)` -- see §7.1).
- **Observable gauges on the daemon:** If the daemon caches state (e.g., open DB connections, queued requests), add observable gauges per lore section 4.5 of the guide.
- **Metric names unchanged:** `dept.quest.*` names carry through. The daemon emits them from inside its handler; operators see no dashboard changes.

The CLI-side design in this document remains the right shape whether or not the daemon ships. The daemon design will be a separate section added to this document when it is specified.

---

## 19. Checklist

### Naming and attributes

- [ ] Root command spans follow `execute_tool quest.{command}` with static names
- [ ] Child spans use the documented `quest.{subsystem}.{operation}` carve-out (§4.2) and do not carry `gen_ai.tool.name`
- [ ] Command spans use SpanKind=INTERNAL today; CLIENT when the `questd` daemon ships (§18)
- [ ] All command spans carry `gen_ai.tool.name`, `gen_ai.operation.name`, `gen_ai.agent.name`, `dept.task.id`, `dept.session.id`, `quest.role.elevated` -- note `quest.command` has been removed (§4.3)
- [ ] `gen_ai.agent.name` and metric `role`/`agent.role` dimension both pass through `roleOrUnset` -- no empty-string vs `"unset"` mismatch
- [ ] `dept.session.id` sourced from `AGENT_SESSION` -- required framework attribute per `otel-guide.md` §3.3
- [ ] `db.system=sqlite` attribute on every DB-touching span, cached with `sync.Once`
- [ ] Metric units are UCUM-style entity names (`{operation}`, `{task}`, `{link}`, `{event}`) -- no cause-style units
- [ ] Histograms use custom bucket boundaries (see 5.2)
- [ ] Combined cardinality ceiling documented for `dept.quest.tasks.created` and `dept.quest.tasks.completed` (§5.3)
- [ ] Content events use the `quest.*` prefix (not `dept.quest.*`); this is intentional (§4.5)

### Errors and classification

- [ ] Error recording uses three-step pattern (RecordError + SetStatus + metric counter)
- [ ] Exit-code to `quest.error.class` mapping applied consistently
- [ ] Exit-code-7 path emits `dept.quest.store.lock_timeouts` increment
- [ ] `dept.quest.errors` vs `dept.quest.operations{status=error}` relationship documented (superset, not sum)

### Privacy

- [ ] Content capture gated on `OTEL_GENAI_CAPTURE_CONTENT`, stored as a plain `bool` set once under `sync.Once`
- [ ] Content events absent when `OTEL_GENAI_CAPTURE_CONTENT` unset
- [ ] All variable-length attributes truncated with UTF-8-safe limits; ID lists via `truncateIDList` (comma-aware)
- [ ] Metric attributes are low-cardinality only (no task IDs, titles, tag values, parent IDs)

### Propagation

- [ ] Trace context extracted from `TRACEPARENT` in CLI once at startup
- [ ] Global W3C composite propagator registered inside `telemetry.Setup` (§7.3)
- [ ] Context propagation through all code paths (existing `context.Context` threading)

### Span coverage

- [ ] `quest.store.tx` span wraps every `BEGIN IMMEDIATE` transaction with `db.system=sqlite`, `quest.tx.lock_wait_ms`, `quest.tx.rows_affected`, and `quest.tx.outcome` attributes
- [ ] `quest.store.traverse` span emitted for graph traversals (`graph`, `deps`, `--ready`)
- [ ] `quest.store.rename_subgraph` span emitted for `quest move`
- [ ] Uniform single-row store ops (`insert_task`, `update_task`, `append_history`, `insert_dep`, `delete_dep`, `update_tags`, `select_task`, `list_tasks`, `export.write`) emit `quest.store.op` **events** on the parent command span -- no child spans (§4.2, §8.3)
- [ ] Store events carry `db.system`, `db.operation`, `db.target`, `rows_affected` (when applicable), and `duration_ms`
- [ ] `quest.validate` span with per-phase children for `quest batch`
- [ ] `quest.role.gate` span emitted for elevated commands
- [ ] `quest.db.migrate` span emitted at startup when schema upgrade runs
- [ ] `quest version` produces no span and no metric increment -- suppressed at dispatch (§4.2)

### SDK lifecycle

- [ ] Service name is `quest-cli` (§7.1, §11)
- [ ] CLI uses `BatchSpanProcessor` with `WithBatchTimeout(1 * time.Second)` -- **never** `SimpleSpanProcessor` in production (guide §7.4)
- [ ] Log processor is `BatchLogRecordProcessor` matching the span-processor cadence
- [ ] No command handler calls `os.Exit` directly -- handlers return an int exit code to `main`, which runs deferred shutdown before calling `os.Exit`
- [ ] SDK init returns shutdown function wired into `defer` in `main`
- [ ] Shutdown ordering: command handler returns int -> deferred spans end -> otelShutdown flushes -> `main` calls `os.Exit`
- [ ] Shutdown is idempotent and timeout-bounded (5s)
- [ ] Partial init failure calls `Shutdown` on already-initialized providers (§7.8)
- [ ] No-op providers installed explicitly when telemetry disabled
- [ ] `otel.SetErrorHandler` routes OTEL internal errors to slog (§7.4) so stderr stays protocol-clean
- [ ] `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` triggers a single slog warn and HTTP fallback (§7.5)
- [ ] `deployment.environment` sourced from `OTEL_RESOURCE_ATTRIBUTES`, not hardcoded (§11)

### Architecture

- [ ] Internal packages import only OTEL API, never SDK (§10.1)
- [ ] `InstrumentedStore` decorator keeps business logic clean
- [ ] `CommandSpan` relies on no-op providers for the disabled path -- no manual short-circuit around `tracer.Start` (§8.2)
- [ ] `AGENT_ROLE`, `AGENT_TASK`, `AGENT_SESSION` read once under `sync.Once`, not per call
- [ ] Recording functions follow `telemetry.RecordX()` pattern (no OTEL imports outside `internal/telemetry`)
- [ ] HTTP exporter only -- no gRPC dependency for the CLI
- [ ] slog bridge composes with existing stderr handler (fan-out), does not replace it
- [ ] Slog calls use `*Context(ctx, ...)` variants for trace correlation
- [ ] Log event inventory (§3.2) is authoritative; new call sites match an existing category
- [ ] Decorators use package-level tracer/meter, not per-struct instances

### Metrics coverage

- [ ] Status-transition counter (`dept.quest.status_transitions`) increments on every status change
- [ ] Batch validation errors emit per-phase counters and span events
- [ ] Cycle detection records the cycle path as a span event (truncated)
- [ ] Observable gauges not required (no long-lived state in the CLI)

### Misc

- [ ] `--no-track` intentionally not implemented (documented rationale in 9.2)
- [ ] Subprocess env-propagation helpers: **not applicable** (quest spawns no subprocesses); revisit if `questd` is introduced
- [ ] `OTEL_SEMCONV_STABILITY_OPT_IN`: not applicable (quest pins semconv v1.40.0)
- [ ] Duration calculation uses `float64(elapsed.Microseconds()) / 1000.0`
- [ ] Semconv imports pinned to `go.opentelemetry.io/otel/semconv/v1.40.0`

### Testing

- [ ] Tests verify no-panic with no-op providers
- [ ] Tests verify instrument creation with real providers
- [ ] Tests assert span names, attributes, and parent-child relationships
- [ ] Exit-code-to-class table covered by tests
- [ ] Tests save/restore OTel global providers via `t.Cleanup`
- [ ] `quest init` produces only a command span (no store operations until after init succeeds)

### Forward compatibility

- [ ] Forward-compatibility with `questd` daemon documented (section 18); span kind CLIENT/SERVER transition explained

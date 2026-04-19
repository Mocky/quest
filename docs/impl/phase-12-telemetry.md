# Phase 12 — Telemetry (OTEL)

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

Follow `OTEL.md` §16 "Implementation Sequence" — it is the canonical order. Each task below covers one or more numbered items in §16.

**§16 step → Phase 12 task map** (auditability per plan review):

| §16 step | Task       | Notes                                                                                    |
| -------- | ---------- | ---------------------------------------------------------------------------------------- |
| 1, 2     | 12.1       | Real `telemetry.Setup`; providers + resource + propagator                                |
| 3        | 12.2       | `CommandSpan` / `WrapCommand` dispatcher-owned                                           |
| 4        | —          | §16 step 4 (`SpanEvent` wrapper) is intentionally dropped per the M5 decision — span events ship only via named recorders (see §8.6 and Task 12.1). Handlers never call a general-purpose `SpanEvent` helper |
| 5        | 12.3       | Role gate span                                                                           |
| 6        | 12.4       | `InstrumentedStore` decorator                                                            |
| 7        | 12.5       | Metrics registration; `RecordHandlerError` / `RecordDispatchError` helpers; `quest.error.class` / `quest.error.retryable` / `quest.exit_code` attribute set |
| 8        | 6.2 etc.   | Status-transition metric wired into each handler (anchored in per-handler recorder calls) |
| 9        | 12.6, 12.11 | Batch validation spans (12.6); `RecordBatchOutcome` + `dept.quest.batch.size` (12.11)   |
| 10       | 12.9       | Query/graph attributes + recorders                                                       |
| 11       | 12.10      | Move/cancel attributes + recorders                                                       |
| 12       | 12.7       | Content capture                                                                          |
| 13       | 12.1, 12.8 | SDK shutdown wiring; migration-end contract test                                          |
| 14       | 13.1–13.4  | Test coverage (contract + structural + concurrency tests)                                |

### Task 12.1 — Real `telemetry.Setup` (tracer/meter/logger providers, fan-out slog bridge)

**Spec anchors:** `OTEL.md` §7 (full section — Setup signature in §7.1), §8.1, §8.8 (migration span is a sibling of the command span), §10.1–10.3, §11.

**Implementation notes:**

Covers §16 steps 1, 2, and 13. Replaces the no-op shell from Task 2.3 with real SDK wiring.

- Conditional: disabled → install explicit no-op providers.
- `Setup` accepts the `telemetry.Config` shape from `OTEL.md` §7.1: `{ServiceName, ServiceVersion, AgentRole, AgentTask, AgentSession, CaptureContent}`. The agent-identity fields come from `cfg.Agent.{Role,Task,Session}`; `CaptureContent` comes from `cfg.Telemetry.CaptureContent`. Both are resolved by `internal/config/` per Task 1.3. Telemetry never calls `os.Getenv` itself.
- `Setup` calls `setIdentity(role, task, session)` once, which stores `roleOrUnset(role)` plus the raw task/session strings for use by every subsequent `CommandSpan` / recorder without locking. `Setup` also caches `cfg.CaptureContent` in a package-level `bool captureContent`, written once inside `sync.Once.Do` per `OTEL.md` §4.5 / §19 (belt-and-suspenders against any future caller that invokes `Setup` twice). Task 12.7's content-recorder gate reads this cached value; checking `os.Getenv` per-call is explicitly avoided.
- Service name `quest-cli`, resource via `semconv/v1.40.0`.
- Span and log processors match `OTEL.md` §7.1 processor configuration exactly: `BatchSpanProcessor` via `sdktrace.WithBatchTimeout(1 * time.Second)`; `BatchLogRecordProcessor` via `sdklog.WithExportInterval(1 * time.Second)`. **Never `SimpleSpanProcessor`.** The two option names differ — span uses `WithBatchTimeout`, log uses `WithExportInterval` — mixing them up is a compile error or a silent no-op on a future SDK release.
- Register the W3C composite propagator + `otel.SetErrorHandler` routing to slog.
- Partial-init cleanup per §7.8.
- `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` → slog warn + HTTP fallback.
- **Shutdown timeout.** The returned shutdown function runs under a caller-supplied context; `main.run()` defers it with `context.WithTimeout(..., 5*time.Second)` per `OTEL.md` §7.1. The 5-second cap upper-bounds the flush on exit so a misconfigured collector cannot hang the CLI.
- Install the `otelslog` bridge as the second child of the logging fan-out. The bridge handler is constructed by `internal/telemetry/` (not `internal/logging/`) and returned to `main.run()` from `Setup` as its first return value; `main.run()` then calls `logging.Setup(cfg.Log, bridge)` (the variadic signature from Task 2.1). This preserves the `OTEL.md` §10.1 rule that only `internal/telemetry/` imports OTEL packages. **The bridge handler must honor `cfg.Log.OTELLevel` independently of the stderr level** — `OBSERVABILITY.md` §Logger Setup: "stderr follows `QUEST_LOG_LEVEL`, the OTEL bridge follows `QUEST_LOG_OTEL_LEVEL`." Apply the level via `otelslog.NewHandler(otelslog.WithLevel(cfg.Log.OTELLevel))` if the contrib package exposes the option; if not, wrap the bridge with a thin `slog.LevelHandler` that gates `Enabled` and `Handle` at `cfg.Log.OTELLevel`. Without this, INFO records (`role gate denied`, `precondition failed`, `batch mode fallthrough`, `schema migration applied`) would be silently filtered out of the OTEL log signal because the fan-out's stderr child gates at `warn` by default. Layer 1 test asserts (a) an INFO record reaches the OTEL exporter when `OTELLevel=info`, (b) the same record does not reach stderr when `Level=warn`. OTEL-level filter defaults to `info`, stderr level default stays `warn`.
- **Implement the real `telemetry.MigrateSpan` and consolidate the migration metric.** Signature unchanged: `MigrateSpan(ctx, from, to int) (context.Context, func(applied int, err error))`. The body:
  1. Opens a `quest.db.migrate` span with `quest.schema.from=from`, `quest.schema.to=to`.
  2. Returns an `end(applied int, err error)` closure that:
     - Sets `quest.schema.applied_count=applied` on the span.
     - Applies the three-step error pattern when `err != nil`.
     - Ends the span.
     - Increments `dept.quest.schema.migrations{from_version=from, to_version=to}` exactly once per migration set applied — never zero, since the dispatcher gates on `from < to` (Task 4.2 step 5) before calling `MigrateSpan`. The metric counts migrations-run, not checks-attempted.
  Callers: the dispatcher (Task 4.2 step 5) and `quest init` (Task 5.1). For the dispatcher path, `quest.db.migrate` lands as a sibling of the command span; for init, as a child (the documented §8.8 carve-out). MigrateSpan owns both the span and the metric in one closure, so "one migration = one span + one metric" is structurally guaranteed — there is only one call site for both, and the dispatcher's `from < to` gate is the single source of truth for whether a migration runs.
- Implement `telemetry.ExtractTraceFromConfig(ctx, traceparent, tracestate string) context.Context` per `OTEL.md` §6.2. Build a `propagation.MapCarrier` with the two strings, call `otel.GetTextMapPropagator().Extract(ctx, carrier)`, return the derived context. **When `traceparent == ""` (regardless of `tracestate`), return `ctx` unchanged.** When `traceparent` is set and `tracestate` is empty, extraction proceeds and the `tracestate` key is simply omitted from the carrier — `tracestate` alone is never a reason to short-circuit.
- **No `telemetry.SpanEvent` wrapper.** Per the M5 decision, the general-purpose `SpanEvent` is removed from the handler-callable surface — every span event is emitted by a named recorder in the §8.6 inventory (`RecordPreconditionFailed`, `RecordCycleDetected`, `RecordBatchError`, etc.). Named recorders apply the `if !span.IsRecording() { return }` short-circuit internally so the §14.5 zero-allocation guarantee is preserved per-recorder. If a future span event is needed that no existing recorder covers, add a new `RecordX` to the §8.6 inventory and wire it here — do not reintroduce a general-purpose wrapper.

**Tests:** Layer 1: disabled path returns no-op providers; partial-init failure shuts down earlier providers; protocol warn fires once; `setIdentity` + `CommandSpan` emit `gen_ai.agent.name="unset"` when `AgentRole` is empty.

**Shared `tracetest` helper.** Phase-12 tests reuse `testutil.NewCapturingTracer(t) (exporter *tracetest.InMemoryExporter, recorder *tracetest.SpanRecorder)` in `internal/testutil/` — the helper constructs the exporter + provider, swaps the global via `otel.SetTracerProvider`, and registers `t.Cleanup` to restore the prior provider. Same helper pattern (and same file) for a capturing meter provider and logger provider. Prevents the exporter setup dance from drifting across test files.

---

### Task 12.2 — `CommandSpan` / `WrapCommand` (dispatcher-owned)

**Spec anchors:** `OTEL.md` §4.2, §4.3, §8.2 (dispatcher-owned; handler-agnostic).

**Implementation notes:**

- Identity attributes come from the `telemetry.Config` passed to `Setup` (Task 12.1); `internal/telemetry/identity.go` holds the cached struct. No `env.go`, no `sync.Once` on env reads, no `os.Getenv` in this package.
- Apply `roleOrUnset` at the `setIdentity` step so `AGENT_ROLE=""` surfaces as the literal `"unset"` on both span attributes (`gen_ai.agent.name`) and metric dimensions (`role`) — the consistency guarantee from `OTEL.md` §8.6 that cross-signal queries depend on.
- Two entry points in `internal/telemetry/command.go`, both used by `cli.Execute` (Task 4.2), not by handlers:
  - Primitive: `ctx, span := telemetry.CommandSpan(parentCtx, "accept", elevated); defer span.End()` — starts the span and returns a context carrying it. The dispatcher always calls this as step 2 of dispatch (see Task 4.2) and owns the `defer span.End()`. The dispatcher is responsible for the §4.4 three-step error pattern for pre-handler errors (config.Validate, store.Open, store.Migrate).
  - Middleware: `return telemetry.WrapCommand(ctx, "accept", handlerFunc)` — picks up the active command span via `trace.SpanFromContext(ctx)` and, on a non-nil error from `fn`, calls `telemetry.RecordHandlerError(ctx, err)` to apply the §4.4 three-step pattern + the C1 attribute set (`quest.error.class`, `quest.error.retryable`, `quest.exit_code`) on the active span and increment `dept.quest.errors{error_class}`. WrapCommand itself increments `dept.quest.operations{status=ok|error}`. Per `OTEL.md` §8.2, **`WrapCommand` does not start a new span and does not call `span.End()`**; it is a no-start/no-end error-handling middleware. No `elevated` parameter — the role gate lives in the dispatcher (Task 4.2 step 3), not inside `WrapCommand`, so the middleware's only job is handler-error observation and counter increments. (Note: this resolves a signature mismatch with `OTEL.md` §8.2's example, which still shows a four-arg form including `elevated bool`. The plan's three-arg form is authoritative; update OTEL.md §8.2's example and the §19 checklist line in lockstep with this task.) If the span in ctx is non-recording (e.g., `SuppressTelemetry=true` descriptors skipped CommandSpan), `WrapCommand` still runs `fn` normally and records the error on whatever span `trace.SpanFromContext(ctx)` returns (the non-recording root span, which swallows the error gracefully).
- Command handlers do not import `CommandSpan` / `WrapCommand`. Handlers call `telemetry.RecordX` (named recorders for every event they emit) and `telemetry.StoreSpan` (for child store spans). Handlers never import `go.opentelemetry.io/otel/trace` or `go.opentelemetry.io/otel/attribute` — per the M5 decision, there is no general-purpose `SpanEvent` helper; span events ship only through named recorders. Their signature stays `func(ctx, cfg, s, args, stdin, stdout, stderr) error` across phases.
- Root span name `execute_tool quest.<command>`; required attributes per §4.3. The primitive opens the span; the middleware observes the result — together they produce a single command span per invocation.

**Tests:** Layer 1 with in-memory exporter from `sdk/trace/tracetest`: assert root span name, `gen_ai.*` attributes present (with `"unset"` when identity is empty), `quest.role.elevated` bool. Assert WrapCommand does NOT call span.End() by passing a dispatcher-style setup (CommandSpan → WrapCommand → span.End on defer) and checking the exporter records exactly one command span with exactly one End event. Plus the grep tripwire as a test: `grep -rn 'go.opentelemetry.io' internal/ cmd/` returns matches only under `internal/telemetry/` — widens the Task 2.3 tripwire scope to the full source tree (matching `OTEL.md` §10.1), so a future accident in `internal/cli/` / `internal/store/` / etc. does not slip through.

---

### Task 12.3 — Role gate span

**Spec anchors:** `OTEL.md` §8.7 (separation-of-concerns: gate decision lives in `internal/cli/`; telemetry observes only).

**Implementation notes:** Replace the no-op `telemetry.GateSpan(ctx, agentRole string, allowed bool)` with the real implementation from `OTEL.md` §8.7. The function starts a `quest.role.gate` span, sets `quest.role.required="elevated"`, `quest.role.actual=roleOrUnset(agentRole)`, and `quest.role.allowed=<bool>`, then ends the span immediately. The function must not import `internal/config/` or evaluate policy — the caller (Task 4.2 step 3 for elevated commands, or the `update` handler for mixed-flag gates) already computed `allowed` via `config.IsElevated`. The span is emitted whether or not the command proceeds: retrospective queries care about attempts, not just denials.

---

### Task 12.4 — `InstrumentedStore` decorator

**Spec anchors:** `OTEL.md` §8.3 (decorator-owned tx span), §4.3 (DB span attributes).

**Implementation notes:**

- Wrap the store. The decorator's instrumented `BeginImmediate(ctx, kind TxKind)` is the single seam for store-side telemetry.
- **Idempotent `WrapStore`.** `WrapStore` checks whether its argument is already an `*InstrumentedStore` and, if so, returns it unchanged. Both the dispatcher (Task 4.2 step 5) and `quest init` (Task 5.1) call `WrapStore` on the opened store — a future handler that copies the init pattern by mistake would otherwise double-wrap and emit duplicate `quest.store.tx` spans. The idempotence check inside `WrapStore` is simpler and safer than a grep-tripwire enforcement of "only one call site."
- **`BeginImmediate` override.** Procedure:
  1. Call `inner.BeginImmediate(ctx, kind)` on the wrapped store, getting a `*store.Tx` whose `invokedAt` and `startedAt` fields are already populated by the bare store (Task 3.1).
  2. Start a `quest.store.tx` span via `tracer.Start(ctx, "quest.store.tx", trace.WithTimestamp(tx.invokedAt), trace.WithAttributes(attribute.String("db.system", "sqlite"), attribute.String("quest.tx.kind", string(kind))))`. The `db.system` `attribute.KeyValue` is cached at package init via `sync.Once` — avoids re-allocating it on every DML-heavy workload.
  3. Populate `tx.onCommit` and `tx.onRollback` hooks: each closure ends the span with `quest.tx.lock_wait_ms = tx.startedAt.Sub(tx.invokedAt).Milliseconds()` and `quest.tx.outcome ∈ {committed, rolled_back_precondition, rolled_back_error}`. The hook reads `invokedAt`/`startedAt` directly from `*store.Tx` — the decorator never re-computes timing from its own clock, so the recorded `lock_wait_ms` excludes decorator overhead (the `tracer.Start` call, the hook installation). Pinning the derivation to `*store.Tx` makes the struct the single source of truth for its own life.
  4. Return the same `*store.Tx` (now with hooks populated).
- `quest.tx.outcome` disambiguates three close paths: `committed` (handler called `tx.Commit()`, underlying `*sql.Tx.Commit()` returned nil), `rolled_back_precondition` (handler returned a typed precondition error — `ErrConflict`, `ErrNotFound`, or `ErrPermission` — and deferred `tx.Rollback()`), `rolled_back_error` (any other error during the transaction — including `sql.ErrTxDone` on a committed-twice bug). **The hook auto-infers from the error the handler returned, so the common case needs no handler-side bookkeeping:** `committed` on Commit-success; `errors.IsAny(err, errors.ErrConflict, errors.ErrNotFound, errors.ErrPermission)` → `rolled_back_precondition`; any other error → `rolled_back_error`. (`errors.IsAny(err, targets ...error) bool` is a small helper added to `internal/errors/` per Task 2.2 — it iterates the targets and returns true on the first `errors.Is` match. Avoids the misleading bitwise-OR pseudo-syntax that doesn't compile in Go.) Handlers can still call `tx.MarkOutcome(store.TxRolledBackError)` to override the inferred classification (reserved for future bespoke error classes); the common precondition paths across Tasks 6.2, 6.3, 6.4, 7.1, 8.1, 8.3, 9.1, 9.2 do not need explicit `MarkOutcome` calls.
- **Exit-7 (lock timeout) records two additional attributes** per `OTEL.md` §4.3. Inside the `onRollback` hook, detect `errors.Is(err, errors.ErrTransient)` (the sentinel the store maps `SQLITE_BUSY` / error code 5 to). When true, set `quest.lock.wait_limit_ms = 5000` (matches the `PRAGMA busy_timeout = 5000` contract) and `quest.lock.wait_actual_ms = tx.startedAt.Sub(tx.invokedAt).Milliseconds()` on the span before the three-step error pattern runs. These attributes anchor the §15 alerting query ("p95 of `quest.store.tx.lock_wait` > 2000ms") to specific traces — without them, the `dept.quest.store.lock_timeouts` counter fires but there is no trace record to drill into.
- **`rows_affected` is populated from the `*store.Tx` accumulator** (Task 3.1's `ExecContext` wrapper sums `RowsAffected()` across DML). The hook reads `tx.rowsAffected` and sets `quest.tx.rows_affected = tx.rowsAffected`. No per-Exec instrumentation needed; accumulation is invisible to handlers.
- **No per-DML `quest.store.op` events.** The decorator does not instrument individual `tx.ExecContext` / `tx.QueryContext` / `tx.QueryRowContext` calls — those pass through to the inner `*sql.Tx` directly. The transaction-level `quest.store.tx` span, plus the three named child spans (`quest.role.gate`, `quest.db.migrate`, `quest.batch.*` phase spans), are the complete store-side instrumentation contract. Dashboards that want per-SQL timing use `EXPLAIN`/slow-log at the DB layer, not span events.
- **`quest.store.traverse` and `quest.store.rename_subgraph` are handler-emitted, not decorator-emitted.** The decorator's only emission point is `quest.store.tx` (from the `BeginImmediate` override and hook). Read methods pass through the decorator unchanged — `CurrentSchemaVersion`, `GetTask`, `GetTags`, `GetPRs`, `GetNotes` emit no traverse spans, since they are fast single-row meta/field reads rather than graph or list traversals. Handlers that do graph/list traversal (`quest graph`, `quest deps`, `quest list`) wrap their traversal reads with `telemetry.StoreSpan(ctx, "quest.store.traverse")` — a thin helper in `internal/telemetry/store.go` that calls `trace.SpanFromContext` + child span creation. `quest move` wraps its FK-cascade UPDATE loop with `telemetry.StoreSpan(ctx, "quest.store.rename_subgraph")`. This keeps the `Store` interface narrow (no read-kind enum), preserves the OTEL import boundary (handlers call a `telemetry` wrapper, never `go.opentelemetry.io/otel/trace` directly), and matches the spec's scoping of `quest.store.traverse` to "graph/list queries" rather than every read.

**Tests:** Layer 1 + Layer 3 with the in-memory exporter: `accept` on a parent produces exactly one `quest.store.tx` span with correct attributes (`db.system=sqlite`, `quest.tx.kind=accept`, `quest.tx.lock_wait_ms` non-negative, `quest.tx.outcome=committed`); concurrent-writer test confirms `lock_wait_ms` reflects actual wait, not decorator overhead; `quest show` produces exactly one `quest.store.traverse` span and no store-tx span (reads go through a different seam).

---

### Task 12.5 — Metrics (`dept.quest.*`) and shared error/dispatch recorders

**Spec anchors:** `OTEL.md` §5 (full section), §4.4 (three-step error pattern + the `quest.error.class` / `quest.error.retryable` / `quest.exit_code` attribute set), §13 (precondition events).

**Implementation notes:** Create every instrument listed in §5.1 at package init inside `internal/telemetry/recorder.go`. Wire increments through the `RecordX` functions that handlers already call (installed as no-ops in Task 2.3). Histogram bucket boundaries per §5.2. **Pin the lock-timeout counter name to `dept.quest.store.lock_timeouts`** (per §5.1, the authoritative declaration) — `OTEL.md` §8.4 step 6 uses the inconsistent `dept.quest.store.tx.lock_timeouts` name in one place; treat §5.1 as authoritative and add a contract test asserting the instrument exists under exactly the §5.1 name to catch any future drift. The absence of `.tx.` is meaningful: `dept.quest.store.tx.duration` and `dept.quest.store.tx.lock_wait` are histograms of normal operation; `dept.quest.store.lock_timeouts` is the error-path counter and is not per-transaction.

**`telemetry.RecordHandlerError(ctx, err)` implementation.** Lives in `internal/telemetry/recorder.go`. Body: pull `span := trace.SpanFromContext(ctx)`; if `span.IsRecording()`, call `span.RecordError(err)`, `span.SetStatus(codes.Error, telemetry.Truncate(err.Error(), 256))`, and `span.SetAttributes(attribute.String("quest.error.class", errors.Class(err)), attribute.Bool("quest.error.retryable", errors.IsRetryable(err)), attribute.Int("quest.exit_code", errors.ExitCode(err)))`. Then increment `dept.quest.errors{error_class=errors.Class(err)}`. This is the single source of truth for error attribute application — both `WrapCommand` (Task 12.2) and `RecordDispatchError` route through here so a future contributor adding a new error site inherits the full attribute set automatically. `errors.IsRetryable` is a one-line helper added to `internal/errors/` that returns true only for `ErrTransient` (exit 7).

**`telemetry.RecordDispatchError(ctx, err, stderr) int` implementation.** Lives in `internal/telemetry/recorder.go`. Body: call `RecordHandlerError(ctx, err)`, then increment `dept.quest.operations{status=error}`, emit `slog.ErrorContext(ctx, "internal error", "err", telemetry.Truncate(err.Error(), 256), "class", errors.Class(err), "origin", "dispatch")` per `OTEL.md` §3.2 (canonical message shared with handler-level panics; the `origin="dispatch"` attribute distinguishes dispatcher-level failures from handler-level ones per the L9 decision), call `errors.EmitStderr(err, stderr)`, return `errors.ExitCode(err)`. The §16 step → task map gains a row: "`quest.error.class` / `quest.error.retryable` / `quest.exit_code` attribute set → Task 12.5". The panic-recovery path in `cli.Execute` (Task 4.2 step 8) emits the same `"internal error"` message with `"origin", "handler"` so retrospectives can split the two.

**Tests:** Instrument-creation test per `OTEL.md` §14.4; exit-code-to-class coverage test per §14.6. New Layer 2 contract test `TestErrorSpanAttributes` iterates exit codes 1–7 and asserts `quest.error.class`, `quest.error.retryable`, `quest.exit_code` are all present on the recorded span. New Layer 2 test `TestErrorMetricSuperset` asserts `sum(dept.quest.errors) == sum(dept.quest.operations{status=error})` for a fixture command that errors with each exit code 1–7.

---

### Task 12.6 — Batch validation spans

**Spec anchors:** `OTEL.md` §8.5, §13.4 (cycle event).

**Implementation notes:** Wrap `quest batch`'s four validation phases in a parent `quest.validate` span with one child span per phase (`quest.batch.parse`, `quest.batch.reference`, `quest.batch.graph`, `quest.batch.semantic`). The resulting tree (matches `OTEL.md` §4.1):

```
execute_tool quest.batch
  └── quest.validate
        ├── quest.batch.parse
        ├── quest.batch.reference
        ├── quest.batch.graph
        └── quest.batch.semantic
```

Each phase span emits a `quest.batch.error` event per validation failure and increments `dept.quest.batch.errors{phase, code}`. **The event attribute set must include every spec-defined field per error code** — not just `(line, code, field, ref?)`. Concretely, share the field-bag emitter between the stderr JSONL writer and the span event by routing both through `telemetry.RecordBatchError(ctx context.Context, fields map[string]any)`: phase 1 fills `line`, `code`, `field`, optional `ref`; per-code additions: `first_line` for `duplicate_ref`, `cycle` for `cycle`, `depth` for `depth_exceeded`, `target` / `actual_status` for `retry_target_status` / `blocked_by_cancelled`, `link_type` / `required_type` for `source_type_required`, `value` for `invalid_tag` / `invalid_link_type`, `id` for `unknown_task_id`. Reusing `output.EmitJSONL`'s data structure prevents stderr-vs-span field drift; a Layer 2 contract test asserts the span-event field coverage equals the stderr field coverage per error code.

The command span records `quest.batch.lines_total`, `quest.batch.lines_blank`, `quest.batch.partial_ok`, `quest.batch.created`, `quest.batch.errors`, and `quest.batch.outcome` per §4.3 (set by `RecordBatchOutcome` from Task 12.11).

**Cycle-detected event.** Phase 3 (`quest.batch.graph`) calls `telemetry.RecordCycleDetected(ctx, path []string)` for every cycle it detects (in addition to the per-line `quest.batch.error` event). The same recorder is used by `quest link --blocked-by` (Task 9.1) when a single-edge add closes a cycle. Per `OTEL.md` §13.4, the recorder emits `quest.dep.cycle_detected` with `quest.cycle.path` (truncated via `truncateIDList`, capped at 512 chars) and `quest.cycle.length`. Cycle paths are diagnostic gold; without the dedicated event, dashboards would have to scrape stderr for the path.

**Tests:** Layer 1 with in-memory exporter: feed a batch covering every error code from spec §Batch error output; assert the phase-to-span mapping, the per-code field coverage on each `quest.batch.error` event, and that `quest.dep.cycle_detected` fires for every cycle case.

---

### Task 12.7 — Content capture

**Spec anchors:** `OTEL.md` §4.5 (content events + truncation limits), §14.2 (gate-before-allocation pattern).

**Implementation notes:**

- `OTEL_GENAI_CAPTURE_CONTENT` is read by `internal/config/` (Task 1.3) and surfaced as `cfg.Telemetry.CaptureContent`. Telemetry caches the value once via `telemetry.Setup` in `setup.go` (package-level `bool captureContent`; no atomic needed — write-once in `Setup`, read-after by recorders). Handlers query the cached value via `telemetry.CaptureContentEnabled() bool` — declared in Task 2.3's stub list (Phase-2 stub returns `false`, Phase-12 implementation reads the cached `bool`). The helper exists from Phase 2 so handler compile sites in Phases 6–11 do not depend on Phase 12 being landed. See Task 12.1 for the cache wiring — this task fills in the recorder bodies.
- Add per-command recorder calls that emit the span events listed in `OTEL.md` §4.5 (`quest.content.title`, `quest.content.description`, `quest.content.context`, `quest.content.acceptance_criteria`, `quest.content.note`, `quest.content.debrief`, `quest.content.handoff`, `quest.content.reason`).
- **Gate at the call site, not inside the recorder.** Pattern: `if telemetry.CaptureContentEnabled() { telemetry.RecordContentTitle(ctx, task.Title) }`. Checking the flag inside the recorder does not save the caller's string evaluation (the argument is already on the stack). Alternatively, a recorder may accept a `func() string` closure so expansion is deferred: `telemetry.RecordContentTitle(ctx, func() string { return task.Title })`. Do not pass raw strings unconditionally. This is the mechanic that backs `OTEL.md` §14.5's "zero allocation when disabled" benchmark; tests in Task 12.7 measure allocations per `RecordContentX` call under both `CaptureContent` states.
- **Content-emitting commands are write-side only:** `create`, `update`, `complete`, `fail`, `cancel`, `reset`, `batch`. `quest show` / `quest list` / `quest graph` are read commands and emit no content events regardless of `OTEL_GENAI_CAPTURE_CONTENT` state — per `OTEL.md` §4.5 and §9.2. A curator repeatedly reading task data would otherwise double-emit (once on the write that set the value, again on every read) and flood the collector.
- Each write command emits one event per captured field it touches; fields not mutated by the call are not emitted.
- Truncation limits per `OTEL.md` §4.5: title 256, description/context/debrief/handoff 1024, note/reason/acceptance_criteria 512. Use the shared `truncate` helper from `internal/telemetry/truncate.go`.

**Tests:** Layer 1 with in-memory exporter: with `CaptureContent=false`, assert no `quest.content.*` events emitted across the command matrix; with `CaptureContent=true`, assert the expected events are emitted with the correct truncation.

---

### Task 12.8 — Migration-end contract test

**Spec anchors:** `OTEL.md` §4.1 (hierarchy), §8.8 (sibling / init-child relationship), §5.1 (`dept.quest.schema.migrations` counter).

**Implementation notes:** `MigrateSpan` (emitter + attributes + metric) lives in Task 12.1 — this task only adds the integration tests that pin the contract end-to-end. Task 12.1's returned `end` closure records both the span's `quest.schema.applied_count` attribute and the `dept.quest.schema.migrations{from_version, to_version}` counter increment in a single place, so "one migration = one span + one metric" is structurally enforced.

**Tests:** Layer 1 + Layer 3 with the in-memory exporter:

- Open a pre-seeded fixture DB at schema version N-1; run any workspace-bound command; assert two root spans on the same trace — `quest.db.migrate` (sibling of the command span) and `execute_tool quest.<name>` — with correct attributes on each. Also assert the migrate span's parent span context equals the command span's parent span context (both anchor to the inbound TRACEPARENT, not to each other).
- **Up-to-date DB emits no migrate span.** Run a workspace-bound command against a DB already at `SupportedSchemaVersion`; assert the exporter captures exactly one root span (the command span) and no `quest.db.migrate` sibling. Also assert `dept.quest.schema.migrations` did not increment. Pins the H1 gate: `MigrateSpan` emits only when `from < to`, so span and metric stay symmetric.
- `quest init` on a fresh workspace produces `quest.db.migrate` as a **child** of `execute_tool quest.init` (the §8.8 carve-out — init runs migration from inside its handler).
- The `dept.quest.schema.migrations{from_version=0, to_version=1}` counter increments exactly once per `quest init`, zero times on subsequent invocations against an already-migrated DB.

---

### Task 12.9 — Query / graph recorders (§16 step 10)

**Spec anchors:** `OTEL.md` §8.6 (`RecordQueryResult`, `RecordGraphResult`), §4.3 (`quest.query.*`, `quest.graph.*` span attributes).

**Implementation notes:** Wire the query/graph side of the per-command span-attribute enrichment and metric recorders. Attribute names come straight from `OTEL.md` §4.3; do not rename or substitute count-style variants:

- `RecordQueryResult(ctx, operation string, resultCount int, filter QueryFilter)` — called from `quest list` and `quest deps` handlers. Sets bounded-enum filter values as comma-joined strings: `quest.query.filter.status`, `quest.query.filter.role`, `quest.query.filter.tier`, `quest.query.filter.type` (each is the filter's accepted values joined by `,` in a stable sorted order; omit the attribute when the filter is unset). Sets `quest.query.ready` (bool) when `--ready` is active. Sets `quest.query.result_count=resultCount`. **Do not emit** `quest.query.filter.tag` or `quest.query.filter.parent` — tag and parent ID values are unbounded and are deliberately excluded from span attributes per `OTEL.md` §4.3. Also do not emit `*_count` variants: bounded-enum filters have low cardinality, so the values themselves are the signal. Increments `dept.quest.query.result_count` histogram.
- `RecordGraphResult(ctx, rootID string, nodeCount, edgeCount, externalCount int, traversalNodes int)` — called from `quest graph` handler. Emits the mandatory task-affecting attribute `quest.task.id=rootID` (per `OTEL.md` §4.3), plus `quest.graph.node_count`, `quest.graph.edge_count`, and `quest.graph.external_count` (count of nodes reached via a dependency edge that are not descendants of the root). Increments `dept.quest.graph.traversal_nodes` histogram with `traversalNodes` — this value lives on the metric only, not as a span attribute, per `OTEL.md` §4.3.
- Both recorders gate on the cached `enabled()` check (no-op when disabled), but the gate is inside `internal/telemetry/` — handlers call the recorders unconditionally.

**Tests:** Layer 1 with in-memory exporter — run `quest list --status open` and `quest graph proj-a1` against a fixture workspace; assert recorded attributes match the handler's resolved filter / traversal result. Assert the excluded attributes (`quest.query.filter.tag`, `quest.query.filter.parent`, `quest.graph.root_id`, `quest.graph.traversal_nodes` as a span attr) are **absent**.

---

### Task 12.10 — Move / cancel recorders (§16 step 11)

**Spec anchors:** `OTEL.md` §8.6 (`RecordMoveOutcome`, `RecordCancelOutcome`), §4.3 (`quest.move.*`, `quest.cancel.*` span attributes).

**Implementation notes:** Attribute names come straight from `OTEL.md` §4.3 — do not invent renamed variants, and reuse the mandatory `quest.task.id` task-affecting row instead of introducing a proprietary `quest.cancel.target_id`:

- `RecordMoveOutcome(ctx, oldID, newID string, subgraphSize int, depUpdates int)` — called from `quest move` handler. Sets `quest.move.old_id=oldID`, `quest.move.new_id=newID`, `quest.move.subgraph_size=subgraphSize` (number of tasks being renamed), `quest.move.dep_updates=depUpdates` (count of rows in the `dependencies` table whose `task_id`/`target_id` columns were rewritten by the FK cascade — scoped specifically to dependency edges, which is the signal `quest move` dashboards are built around; do not substitute a coarser total-rows-renamed count). Attribute values are opaque IDs; cardinality-bounded by project size.
- `RecordCancelOutcome(ctx, targetID string, recursive bool, cancelledCount, skippedCount int)` — called from `quest cancel` handler. Emits the mandatory task-affecting attribute `quest.task.id=targetID` (per `OTEL.md` §4.3). Sets `quest.cancel.recursive=recursive`, `quest.cancel.cancelled_count=cancelledCount`, `quest.cancel.skipped_count=skippedCount`. Do not emit a proprietary `quest.cancel.target_id`; the task-affecting row already covers it.
- Both recorders are gated via the same `enabled()` pattern.

**Tests:** Layer 1 with in-memory exporter — `quest move proj-a1 --parent proj-b` against a three-level fixture subgraph; assert `quest.move.subgraph_size` equals the count of tasks moved and `quest.move.dep_updates` equals the number of dependency-table rows updated. `quest cancel -r proj-a1` with mixed descendants; assert `cancelled_count` and `skipped_count` match the response object, and `quest.task.id` carries the cancel target.

---

### Task 12.11 — Batch outcome recorder + `dept.quest.batch.size` histogram

**Spec anchors:** `OTEL.md` §5.1 (`dept.quest.batch.size` instrument with `outcome ∈ {ok, partial, rejected}`), §8.6 (`RecordBatchOutcome`), §16 step 9 (batch-specific recorders).

**Implementation notes:**

- `RecordBatchOutcome(ctx, linesTotal, linesBlank int, partialOK bool, createdCount, errorsCount int)` — called from `quest batch` (Task 7.3) at handler exit, after the creation transaction commits or aborts. Body:
  - Compute `outcome string`: `"rejected"` when nothing was created and validation produced any error (`createdCount==0 && errorsCount>0`); `"partial"` when some were created and some failed under `--partial-ok` (`createdCount>0 && errorsCount>0 && partialOK`); `"ok"` when all submitted lines were created (`createdCount>0 && errorsCount==0`).
  - Record histogram `dept.quest.batch.size{outcome}` with the value `createdCount` (the number of tasks actually created — operators correlate this against `linesTotal` via the span attributes).
  - Set the §4.3 `quest.batch.*` attributes on the command span: `quest.batch.lines_total`, `quest.batch.lines_blank`, `quest.batch.partial_ok` (bool), `quest.batch.created`, `quest.batch.errors`, `quest.batch.outcome` (the same `outcome` string).
- The recorder is the single source of truth for batch outcome classification — Task 7.3's batch handler does not duplicate the outcome math.
- Wired from Task 7.3 at the handler's exit point (single call site), regardless of `--partial-ok` mode. Add the recorder to Task 2.3's stub list (no-op until Phase 12).

**Tests:** Layer 1 with in-memory exporter and meter — feed three fixture batches (all-ok, partial-ok, all-rejected) and assert the `outcome` dimension is set correctly on the histogram and the span attribute set matches §4.3. Extend `TestHandlerRecorderWiring` to require `RecordBatchOutcome` from the batch handler.

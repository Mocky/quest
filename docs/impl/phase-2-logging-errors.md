# Phase 2 — Logging and errors

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

**Implementation order:** Phase 2 has four ordered entries, where the first (Task 2.0) is a prerequisite that exists in code form to satisfy a circular import otherwise hidden in prose: 2.0 → 2.3 → 2.1 → 2.2.

### Task 2.0 — Store interface declaration (Phase-2 prerequisite)

**Deliverable:** `internal/store/store.go` containing only the `type Store interface { ... }` declaration plus the `store.TxKind` and `store.TxOutcome` enums. No `*sqliteStore`, no method implementations.

**Rationale.** Task 2.3 imports `store.Store` (via `telemetry.WrapStore` in the no-op shell), so the `Store` type identifier must exist before Phase 2 telemetry code lands or Phase 2 will not compile. Task 3.1 / 3.3 flesh out the concrete type and stub method bodies in Phase 3; this task only ensures the type-name lookup succeeds. Promoting the constraint to its own numbered task makes the dependency graph explicit instead of buried in a paragraph — an agent skimming "do Phase 2 top-to-bottom" cannot miss it.

**Done when:** `go build ./...` succeeds with `internal/store/store.go` containing the interface declaration and enums but no method bodies.

---

### Task 2.1 — `internal/logging/`: slog stderr handler + level/format parsing

**Deliverable:** `logging.NewStderrHandler(cfg StderrConfig) slog.Handler`, `logging.LevelFromString(s string) (slog.Level, bool)`, a fan-out handler type that wraps N children, and a `logging.Setup(cfg config.LogConfig, extra ...slog.Handler) *slog.Logger` entry point.

**Spec anchors:** `OBSERVABILITY.md` §Logger Setup, §Log Levels, §Standard Field Names; `OTEL.md` §3.1 (the OTEL bridge plugs into this fan-out in Task 12.1).

**Implementation notes:**

- Use `slog.NewTextHandler` under the hood for stderr. Quest does not need JSON logs on stderr — human-readable is the contract.
- The fan-out handler accepts `[]slog.Handler` and dispatches every `Handle`/`Enabled`/`WithAttrs`/`WithGroup` call to every child. It is level-gated at each child, not centrally.
- **Variadic signature.** `logging.Setup(cfg config.LogConfig, extra ...slog.Handler) *slog.Logger` composes `stderrHandler` plus every `extra` handler into the fan-out. Phase 2 callers pass no extras. Task 12.1's `main.run()` calls `logging.Setup(cfg.Log, otelBridge)` where `otelBridge` is constructed by `internal/telemetry/` and returned as a `slog.Handler`. This keeps the fan-out immutable (no `AddHandler` mutation on a live slog pipeline) and preserves the import boundary: `internal/logging/` never imports OTEL.
- Register the resulting logger via `slog.SetDefault(...)` in `cmd/quest/run()` so that non-context call sites (config loading, SDK init) work without extra plumbing.
- **Trace-ID enrichment on stderr.** Per `OBSERVABILITY.md` §Correlation Identifiers, stderr slog records must carry `trace_id` and `span_id` when a span is active on the record's context. Implement this with a thin `traceEnrichHandler` that wraps the stderr text handler: in `Handle(ctx, r)`, call a `telemetry.TraceIDsFromContext(ctx) (traceID, spanID string, ok bool)` helper and, if `ok`, `r.AddAttrs(slog.String("trace_id", traceID), slog.String("span_id", spanID))` before delegating to the child.
- Keep the OTEL API import inside `internal/telemetry/` per `OTEL.md` §10.1. The Phase 2 no-op `telemetry.TraceIDsFromContext` returns `("", "", false)`, so `internal/logging/` never imports `go.opentelemetry.io/otel/trace` directly. Task 12.1 replaces the helper with a real implementation backed by `trace.SpanContextFromContext`; the stderr handler code does not change.
- In the no-op / disabled path, the helper returns `ok=false` and stderr records simply omit the trace fields, matching `OBSERVABILITY.md` §Correlation Identifiers "when available".

**Tests:** Layer 1 tests:

- Level parsing: `"debug"`, `"DEBUG"`, `"info"`, `"warn"`, `"error"` all return `(<level>, true)`; `""` returns `(slog.LevelInfo, false)` and lets the caller decide whether empty is "use default" or "reject"; `"garbage"` returns `(slog.LevelInfo, false)` so `config.Validate` can flag the typo via `if _, ok := logging.LevelFromString(cfg.Log.Level); !ok { ... }`.
- Fan-out: two mock handlers both receive a record; `Enabled` returns true if any child returns true.
- Level gating: a child with `Info` level does not see `Debug` records.

**Done when:** `slog.DebugContext(ctx, "x")` in a handler writes to stderr when `--log-level=debug`, nothing at default.

---

### Task 2.2 — `internal/errors/`: exit-code and error-class mapping

**Deliverable:** `errors.Class(err error) string`, `errors.ExitCode(err error) int`, and sentinel values `ErrUsage`, `ErrNotFound`, `ErrPermission`, `ErrConflict`, `ErrRoleDenied`, `ErrTransient`, `ErrGeneral`.

**Spec anchors:** `quest-spec.md` §Exit codes, `OTEL.md` §4.4 (class vocabulary), `STANDARDS.md` §Exit Code Stability, `OBSERVABILITY.md` §Exit Codes.

**Implementation notes:**

- Sentinels are typed errors (not string sentinels) so `errors.Is`/`errors.As` work through wrapping.
- Define one table that is the single source of truth:
  ```go
  type classInfo struct{ exit int; class string; retryable bool }
  var classes = []classInfo{
      {1, "general_failure",  false},
      {2, "usage_error",      false},
      {3, "not_found",        false},
      {4, "permission_denied",false},
      {5, "conflict",         false},
      {6, "role_denied",      false},
      {7, "transient_failure",true},
  }
  ```
- `ExitCode(err)` walks the chain with `errors.Is` against each sentinel and returns the matching exit code. Unknown errors return 1 (`general_failure`).
- `UserMessage(err error) string` produces the sanitized one-liner that goes to stderr per `OBSERVABILITY.md` §Sanitization.
- `EmitStderr(err error, stderr io.Writer)` writes the two-line `quest: <class>: <message>` + `quest: exit N (<class>)` tail. This is the only place that formats that tail; handlers never write it directly.

**Tests:** Layer 1 + Layer 2 (contract):

- Table-driven test pinning each exit code to its class string. `TESTING.md` §Layer 2 calls this out as a non-negotiable tripwire — implement `TestExitCodeStability` per the example in `STANDARDS.md` §CLI Output Contract Tests.
- Wrapping preserves the mapping: `ExitCode(fmt.Errorf("wrap: %w", ErrConflict)) == 5`.
- Unknown error → exit 1.

**Done when:** the contract test compiles and passes, and every `os.Exit(N)` outside `main` has been replaced by returning a wrapped sentinel.

---

### Task 2.3 — `internal/telemetry/`: no-op shell

**Deliverable:** the package exposes `telemetry.Setup`, `telemetry.CommandSpan`, `telemetry.WrapCommand`, `telemetry.GateSpan`, `telemetry.MigrateSpan`, `telemetry.ExtractTraceFromConfig`, a family of `RecordX` stubs, `telemetry.RecordHandlerError`, `telemetry.RecordDispatchError`, `telemetry.CaptureContentEnabled`, and `telemetry.TraceIDsFromContext`. Signatures below. All bodies are no-ops in Phase 2; Task 12 replaces them. **`SpanEvent` is deliberately absent from the handler-callable surface** — per the M5 decision, every span event goes through a named recorder (`RecordPreconditionFailed`, `RecordCycleDetected`, `RecordBatchError`, etc.). Handlers never emit ad-hoc span events; if a new event is needed, propose a new named recorder in the §8.6 inventory first.

```go
type Config struct {
    ServiceName    string
    ServiceVersion string
    AgentRole      string
    AgentTask      string
    AgentSession   string
    CaptureContent bool
}

func Setup(ctx context.Context, cfg Config) (bridge slog.Handler, shutdown func(context.Context) error, err error)
func ExtractTraceFromConfig(ctx context.Context, traceparent, tracestate string) context.Context
func CommandSpan(ctx context.Context, cmd string, elevated bool) (context.Context, trace.Span)
func WrapCommand(ctx context.Context, cmd string, fn func(context.Context) error) error
func GateSpan(ctx context.Context, agentRole string, allowed bool)
func MigrateSpan(ctx context.Context, from, to int) (context.Context, func(applied int, err error))
func StoreSpan(ctx context.Context, name string) (context.Context, func(err error))
func TraceIDsFromContext(ctx context.Context) (traceID, spanID string, ok bool)
func CaptureContentEnabled() bool
func WrapStore(s store.Store) store.Store
// RecordTaskContext sets the §4.3 task-affecting attributes (quest.task.id, tier, type)
// on the active command span. Called from every task-affecting handler (show, accept,
// update, complete, fail, cancel, move, deps, tag, untag, graph).
func RecordTaskContext(ctx context.Context, id, tier, taskType string)
// RecordHandlerError implements the full §4.4 + §13 pattern on the active span:
// span.RecordError(err), span.SetStatus(codes.Error, ...), and sets the three
// required §4.4 attributes (quest.error.class, quest.error.retryable, quest.exit_code)
// from the err's class/exit-code mapping. Increments dept.quest.errors{error_class}.
// Called from WrapCommand (Task 12.2) and indirectly via RecordDispatchError.
func RecordHandlerError(ctx context.Context, err error)
// RecordDispatchError is the dispatcher-side helper that replaces internal/cli/errorExit.
// It calls RecordHandlerError, increments dept.quest.operations{status=error}, emits the
// "internal error" slog record (per OTEL.md §3.2 — same canonical message for dispatcher-
// and handler-level unexpected errors; an optional origin="dispatch"|"handler" attribute
// may distinguish them in retrospectives), writes the stderr two-liner via
// errors.EmitStderr, and returns errors.ExitCode(err). Lives in internal/telemetry/ so
// internal/cli/ never imports OTEL packages directly (preserves §10.1).
func RecordDispatchError(ctx context.Context, err error, stderr io.Writer) int
// RecordPreconditionFailed emits the §13.3 quest.precondition.failed span event with
// quest.precondition (bounded enum), quest.blocked_by_count, and a truncated
// quest.blocked_by_ids attribute. Called from every exit-5 path in handlers.
func RecordPreconditionFailed(ctx context.Context, precondition string, blockedByIDs []string)
// RecordCycleDetected emits the §13.4 quest.dep.cycle_detected span event with
// quest.cycle.path (truncated via truncateIDList, capped at 512 chars per §13.4) and
// quest.cycle.length. Called from quest link --blocked-by and quest batch graph phase.
func RecordCycleDetected(ctx context.Context, path []string)
// RecordTerminalState emits dept.quest.tasks.completed{tier, role, outcome} for every
// terminal-state arrival. Called once per task transitioned to complete/failed/cancelled
// (cancel -r calls it once per descendant transitioned). Replaces the never-defined
// RecordTaskCompleted referenced in early plan drafts.
func RecordTerminalState(ctx context.Context, taskID, tier, role, outcome string)
// plus one RecordX per observable event (see OTEL.md §8.6)
```

`telemetry.StoreSpan(ctx, name)` is the handler-side wrapper used by `quest move` (`quest.store.rename_subgraph`) and the query/graph handlers (`quest.store.traverse`) — it starts a child span under the command span, returns an `end(err)` closure that applies the three-step error pattern, and ends the span. Handlers never import `go.opentelemetry.io/otel/trace` or `go.opentelemetry.io/otel/attribute` directly — the import boundary in `OTEL.md` §10.1 and the Task 2.3 grep tripwire both depend on this. Span events are emitted only through named recorders (`RecordPreconditionFailed`, `RecordCycleDetected`, `RecordBatchError`, etc.); there is no general-purpose `SpanEvent` handlers can call.

`Setup` returns the `otelslog` bridge handler as its first return value (nil in the disabled path) so `main.run()` can pass it into `logging.Setup(cfg.Log, bridge)` without `internal/logging/` ever importing OTEL. `MigrateSpan` takes both the stored `from` version and the supported `to` version so the span's `quest.schema.from`/`quest.schema.to` attributes are set at `tracer.Start` time per `OTEL.md` §8.8. `ExtractTraceFromConfig` exists from Phase 2 because `cli.Execute` calls it before dispatch (`OTEL.md` §6.2 / §7.1); in Phase 2 it returns `ctx` unchanged, Phase 12.1 swaps in the real implementation.

`WrapStore` is a stub too — in Phase 2 it returns the argument unchanged; in Phase 12.4 it returns the instrumented decorator when telemetry is enabled and the bare store when disabled. Having it present as a stub lets `cli.Execute` (Task 4.2) call it at the single construction site from day one.

`telemetry.Config` matches `OTEL.md` §7.1 — service metadata plus the resolved `cfg.Agent.{Role,Task,Session}` strings. Telemetry never calls `os.Getenv`; identity arrives by parameter.

**Spec anchors:** `OTEL.md` §7.1 (Config shape), §8.1 (package layout), §8.2 (dispatcher-owned `CommandSpan` / `WrapCommand`), §8.3 (`enabled` is package-private), §8.6 (recorder functions + `roleOrUnset`), §8.7 (`GateSpan` thin-shape), §10.1 (API-only import boundary).

**Implementation notes:**

- In Phase 2, `Setup` returns a nil bridge handler, a no-op shutdown, and installs nothing. `CommandSpan` returns the input context and `trace.SpanFromContext(ctx)` (the non-recording background span — valid to `End()` / `SetStatus` and cheap). `WrapCommand` in its Phase-2 stub simply calls `fn(ctx)` and returns its error; note that the real Phase-12 version is also a no-start/no-end middleware (per `OTEL.md` §8.2) — it picks up the active span via `trace.SpanFromContext(ctx)` and applies the three-step error pattern, never calling `span.End()`. `GateSpan` returns without recording. `MigrateSpan` returns the input context and a no-op end function. `ExtractTraceFromConfig` returns the input ctx. `StoreSpan` returns the input context and a no-op `end(err error)` closure; Phase-12 fills it in as a thin wrapper that opens a child span under the current command span. Phase-2 callers should still gate content expansion at the call site (see Task 12.7) so stub-vs-real behavior is indistinguishable from the caller's side. `WrapStore` returns its argument. `RecordX` and `TraceIDsFromContext` are empty. This lets the dispatcher (Task 4.2) and command handlers call the real signatures from day one, so Task 12 is a drop-in replacement.
- **No `context.go` file.** The package layout is `setup.go`, `identity.go`, `propagation.go`, `command.go`, `gate.go`, `migrate.go`, `recorder.go`, `store.go`, `validation.go`, `truncate.go` (matches `OTEL.md` §8.1). Quest does not need explicit context keys — the command span lives on `context.Context` via the SDK, and handlers pull it with `trace.SpanFromContext(ctx)`.
- **`validation.go` and `store.go` ship as empty `package telemetry` declarations at Phase 2.** Contents land in Tasks 12.4 (`store.go` — the `InstrumentedStore` decorator) and 12.6 (`validation.go` — the `quest.validate` span + phase children). The files are present in Phase 2 to lock in the OTEL.md §8.1 file inventory but carry no symbols the Phase-2 callers invoke.
- The stub _may_ import `go.opentelemetry.io/otel/trace` for the `trace.Span` return type (API-only, not SDK) — that matches `OTEL.md` §10.1's "API yes, SDK only in setup.go" boundary. **`go.opentelemetry.io/otel/attribute` is never imported outside `internal/telemetry/`** — that's the other half of the M5 boundary. All other OTEL imports stay out until Task 12.1.
- **Who calls what.** `CommandSpan` and `WrapCommand` are **dispatcher primitives** — `cli.Execute` calls them; command handlers do not. Handlers call `telemetry.RecordX`, `telemetry.GateSpan` (only from `quest update`'s mixed-flag path), and `telemetry.StoreSpan` (when they need to open a child span under the command span for a store-level operation, e.g., `quest.store.traverse` / `quest.store.rename_subgraph`). Handlers never import `go.opentelemetry.io/otel/trace` or `go.opentelemetry.io/otel/attribute` — every span event is emitted by a named recorder in the §8.6 inventory; there is no general-purpose `SpanEvent` escape hatch. This preserves the §10.1 boundary and makes the Task 2.3 grep tripwire a durable enforcement mechanism, not documentation fiction.
- **No `Enabled()` export.** The enabled-check exists only as a package-private `enabled()` helper used by `WrapStore` (`OTEL.md` §8.3) to skip the store decorator. Callers must not gate on it — the no-op SDK providers make every `RecordX` / `CommandSpan` / `GateSpan` call already-cheap when telemetry is disabled.
- **`roleOrUnset` applied at the cache-load step.** `Setup` calls an internal `setIdentity(role, task, session)` that stores `roleOrUnset(role)` once; every subsequent span attribute and metric dimension carrying role uses that cached value. Empty `AGENT_ROLE` therefore renders as the literal string `"unset"` consistently across spans and metrics per `OTEL.md` §8.6. Wire this into the Phase 2 stub so the symbol is present even though the attributes are not yet emitted.
- **`telemetry.Truncate(s string, max int) string` is exported** from `internal/telemetry/truncate.go`. UTF-8-safe truncation per `OTEL.md` §3.6 is the shared helper for span attribute values *and* for slog `err` fields (per `OBSERVABILITY.md` §Error Handling, which shows `truncate(err.Error(), 256)` in handler code). Handlers that need to truncate an error message for an slog field import `telemetry.Truncate` — this is the single source of UTF-8-safe truncation in the codebase. Do not re-implement in `internal/errors/` or `internal/logging/`.

**Tests:** Layer 1 — call each stub, assert no panic, no allocation in the disabled path (benchmarks land in Task 12.5).

**Done when:** `grep -r "go.opentelemetry.io" internal/ cmd/` finds nothing outside `internal/telemetry/`; the dispatcher and every command handler call the intended telemetry entry points with signatures that will not change in Phase 12.

# Phase 4 — CLI skeleton

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 4.1 — ID generator (`internal/ids/generator.go`)

**Deliverable:** `ids.NewTopLevel(ctx, tx *store.Tx, prefix string) (string, error)` and `ids.NewSubTask(ctx, tx *store.Tx, parent string) (string, error)`. These are the sole owners of ID allocation — both counter SQL (read-modify-write against `task_counter` / `subtask_counter`) and base36/base10 formatting live here. They are called _inside_ a transaction so concurrent allocators can't collide. The `*store.Tx` wrapper exposes `ExecContext`/`QueryContext`/`QueryRowContext` (pass-through to the embedded `*sql.Tx`), so the helpers remain driver-agnostic.

**Spec anchors:** `quest-spec.md` §Task IDs (format, base36 for top-level, base10 for sub-tasks, 2-char min width, 3-level depth cap, structural immutability).

**Implementation notes:**

- Top-level: increment `task_counter[prefix].next_value`; format as base36, left-pad to min width 2. Concretely: values 1–35 render as `01`–`0z`; 36–1295 render as `10`–`zz`; 1296 is the first 3-char ID and renders as `100`; 46656 is the first 4-char ID and renders as `1000`.
  SQL: `INSERT INTO task_counter(prefix, next_value) VALUES (?, 1) ON CONFLICT(prefix) DO UPDATE SET next_value = task_counter.next_value + 1 RETURNING next_value`. Single round-trip; seeds at 1 for the first top-level task; subsequent calls bump by 1.
- Sub-task: increment `subtask_counter[parent_id].next_value`; format as base10 without padding. ID = `parent + "." + N`.
  SQL: `INSERT INTO subtask_counter(parent_id, next_value) VALUES (?, 1) ON CONFLICT(parent_id) DO UPDATE SET next_value = subtask_counter.next_value + 1 RETURNING next_value`. Same shape as top-level: first sub-task of a given parent seeds the row at 1; later sub-tasks bump. `RETURNING` is stable in SQLite since 3.35 (2021); the pinned `modernc.org/sqlite` minimum (v1.28.0, SQLite 3.45+) is well past that threshold — no fallback is needed. If a future driver downgrade is ever considered, bump the pin rather than adding an untested fallback path.
- Expose `ids.Depth(id string) int` and `ids.ValidateDepth(id string) error` — depth is the count of `.` segments + 1. Reject depth > 3.
- `ids.Parent(id string) string` returns the parent ID (`"proj-01.1.2"` → `"proj-01.1"`, `"proj-01"` → `""`). This is what the move / graph commands use.
- Counters live in the same transaction as the task insert so that a rolled-back insert does not permanently consume an ID.
- Short-ID width is monotonically non-decreasing: the minimum is 2, but once the counter crosses 1296 (`zz`) the formatter naturally produces 3-char IDs. Do not retroactively rewrite older 2-char IDs.

**Tests:** Layer 1:

- `ValidateDepth("proj-01") == nil`, depth 2, depth 3 OK; depth 4 returns error.
- `Parent` for every level.
- Base36 formatting: round-trip the first 1500 values; assert width 2 for 1..1295, width 3 for 1296..46655.

Layer 3 (with store): concurrent `ids.NewTopLevel` calls return distinct IDs — 50 goroutines, each opening its own transaction via `BeginImmediate`, all calling `ids.NewTopLevel(ctx, tx, prefix)`; assert 50 distinct IDs returned with no duplicates.

**Done when:** unit + integration tests pass; ID collisions are structurally impossible under normal use.

---

### Task 4.2 — Global flag parsing + command dispatcher (`internal/cli/`)

**Deliverable:** `cli.Execute(ctx, args []string, stdin io.Reader, stdout, stderr io.Writer) int` — the single entry point called from `main.run()`.

**Spec anchors:** `STANDARDS.md` §Flag Overrides (global flags are position-independent), `quest-spec.md` §Output & Error Conventions, `OBSERVABILITY.md` §Output Contract; `OTEL.md` §4.1 (span hierarchy), §4.2 (version suppressed), §8.2 (dispatcher owns `CommandSpan` / `WrapCommand`), §8.7 (gate-decision-vs-gate-span separation), §8.8 (migration span is sibling of command span).

**Implementation notes:**

- Global flag parsing (`--format`, `--log-level`) lives in an `internal/cli/flags.go` helper exported for `main.run()` to call. Helper signature: `func ParseGlobals(args []string) (config.Flags, []string)` — returns the resolved flags and the stripped (command-name-first) arg list. The helper runs a hand-rolled parse pass that ignores unknown flags (they belong to the subcommand) and returns both a resolved `config.Flags` value and the remaining non-global positional arguments. `cli.Execute` assumes `args` has already had the globals stripped upstream via `cli.ParseGlobals` — it does **not** re-parse. `--color` is not a flag in v0.1 (see cross-cutting rules).
- The first entry of the already-stripped `args` is the command name. Everything else goes to a per-command parser inside the handler.
- Dispatch table: `map[string]commandDescriptor`. Each descriptor carries:
  - `Name string` — the command token.
  - `Handler func(ctx, cfg, s store.Store, args, stdin, stdout, stderr) error` — the command function. **Handlers with `RequiresWorkspace=false` (today: `version`, `init`) receive `s == nil`** because the dispatcher never opens the store for them. `version`'s handler has nothing to do with the store; `init` opens its own store inside the handler (it is the command that creates the workspace). Both handlers must not dereference `s` before checking nil — add a one-line nil-guard at the top of `version.go` and `init.go`. Layer 4 tests assert no nil-deref panic on either command in a workspaceless directory.
  - `Elevated bool` — requires a role listed in `elevated_roles`. The dispatcher runs the role gate (step 3) before any workspace-state work when this is true; non-elevated descriptors skip the gate at dispatch level (e.g., `update`, which runs its own mixed-flag gate inside the handler).
  - `RequiresWorkspace bool` — `true` for every command except `version` and `init`. Drives whether the dispatcher calls `config.Validate`, opens the store, and runs `store.Migrate` before the command.
  - `SuppressTelemetry bool` — `true` for `version` only (extensible to any future purely-informational command). Drives the version suppression in `OTEL.md` §4.2; the dispatcher skips `telemetry.WrapCommand` and does not increment the operations counter for descriptors with this flag set.
- **Descriptor inventory.** The authoritative list for `TestRoleGateDenials` (Task 13.1) and the dispatch table:

  | Command | Elevated | RequiresWorkspace | SuppressTelemetry |
  | ------- | :------: | :---------------: | :---------------: |
  | `version` | false | false | true |
  | `init` | false | false | false |
  | `show` | false | true | false |
  | `accept` | false | true | false |
  | `update` | false (mixed-flag gate inside the handler) | true | false |
  | `complete` | false | true | false |
  | `fail` | false | true | false |
  | `create` | true | true | false |
  | `batch` | true | true | false |
  | `cancel` | true | true | false |
  | `reset` | true | true | false |
  | `move` | true | true | false |
  | `link` | true | true | false |
  | `unlink` | true | true | false |
  | `tag` | true | true | false |
  | `untag` | true | true | false |
  | `deps` | true | true | false |
  | `list` | true | true | false |
  | `graph` | true | true | false |
  | `export` | true | true | false |

  `quest update` is worker-level at dispatch (so workers can call `--note` / `--pr` / `--handoff`), but any elevated flag inside the handler re-runs the role gate and emits its own `quest.role.gate` span — see Task 6.3.
- On an unknown command: return `ErrUsage` (exit 2) with a message listing valid commands (role-filtered per step 1 above) and a "did you mean" suggestion from `cli.Suggest` when a close match exists. Message shape: `unknown command 'stauts'; did you mean 'status'?` followed by the valid-command enumeration. On missing workspace when `RequiresWorkspace` is true: `ErrUsage` with `"not in a quest workspace — run 'quest init --prefix PFX' first"` (exit 2). `quest init` and `quest version` proceed.
- **`cli.Suggest(bad string, valid []string) string`** lives in `internal/cli/suggest.go` and returns the closest match from `valid` when the Levenshtein edit distance is ≤ 2 or ≤ half the input length (whichever is larger; minimum threshold is 2 — short inputs of length 0 or 1 still get a 2-distance grace window because half is 0). Used by: this step's unknown-command rejection, Task 10.2's `--status` / `--type` / `--tier` / `--columns` validation, and any future unknown-enum error site. One helper keeps the "did you mean" error shape consistent across the CLI surface. Implementation is ~15 lines of standard Levenshtein — no external dependency.
- **`cli.Execute` signature.** Takes `cfg config.Config` as a parameter rather than calling `config.Load` itself: `cli.Execute(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int`. `main.run()` is the single caller — it parses the global flags (see Task 0.2 `main.run` flow), calls `config.Load(flags)`, runs `telemetry.Setup`, calls `telemetry.ExtractTraceFromConfig` on the resolved `cfg.Agent.TraceParent`/`TraceState`, then passes `ctx` (carrying the inbound W3C trace context) and `cfg` into `cli.Execute`. No double-load, no double-parse.
- **Dispatch sequence** (this ordering is load-bearing — parenthood in `OTEL.md` §4.1 and the sibling relationship in §8.8 depend on it; the role gate sits above workspace-state operations per spec §Error precedence's "role denial is uniform regardless of workspace state" rule):
  1. Identify the command token from `args`. Unknown command → return `ErrUsage` (exit 2) with a role-filtered banner plus a `cli.Suggest` "did you mean" hint when a close match exists — workers see only the worker commands; elevated roles see the full list. This matches the role-gating rationale: don't leak planner commands to workers.
  2. Look up the descriptor. Save the inbound context: `parentCtx := ctx` (this carries the vigil `TRACEPARENT` per `main.run`'s `ExtractTraceFromConfig` step; it is **not** the command span). Open the command span: `ctx, span := telemetry.CommandSpan(parentCtx, descriptor.Name, descriptor.Elevated); defer span.End()` (skipped entirely when `descriptor.SuppressTelemetry == true`, which today covers only `version`). Opening the span here — before the role gate, config validation, and migration — means every pre-handler error (role-denied, config-invalid, migration-failed) records on this span via the three-step error pattern, and `dept.quest.operations` / `dept.quest.errors` include them. The span's duration therefore covers total dispatcher work (parse → handler return), not just the handler body; see `OTEL.md` §4.2 for the dashboard-meaning note. **Critical:** keep `parentCtx` in scope — migration (step 5) uses it, not the command-span-bearing `ctx`, so the migrate span is a sibling of the command span per `OTEL.md` §8.8.
  3. **Role gate (elevated commands only).** If `descriptor.Elevated == true`, compute `allowed := config.IsElevated(cfg.Agent.Role, cfg.Workspace.ElevatedRoles)` and emit `telemetry.GateSpan(ctx, cfg.Agent.Role, allowed)` as a child of the command span. If `!allowed`, log `role gate denied` at INFO with the attribute set pinned in `OTEL.md` §3.2 (`command`, `agent.role`, `required=elevated`) and return `telemetry.RecordDispatchError(ctx, errors.ErrRoleDenied, stderr)` (exit 6; the helper handles the §4.4 three-step error pattern + the C1 attribute set, the operations/errors counter increments, and the stderr line — see Task 12.5). **This runs before `cfg.Validate` (step 4) and `store.Open`/migrate (step 5), so role denial is uniform regardless of workspace state** — a worker invoking any elevated command gets exit 6 whether the workspace config is valid, the DB needs migrating, or the DB is at a newer-than-supported schema version. Spec §Error precedence requires this: "role denial is uniform regardless of workspace state." For non-elevated descriptors (today: `version`, `init`, `show`, `accept`, `update`, `complete`, `fail`), skip the gate entirely at this step. `quest update`'s mixed-flag gate still fires later from inside its handler (Task 6.3) — the dispatcher-level gate only applies to descriptors with `Elevated: true`.
  4. If `descriptor.RequiresWorkspace`, call `cfg.Validate()`; on error, return `telemetry.RecordDispatchError(ctx, err, stderr)` (exit 2) with the collected validation errors. `version` and `init` skip `Validate`.
  5. If `descriptor.RequiresWorkspace`:
     ```go
     s, err := store.Open(cfg.Workspace.DBPath)
     if err != nil {
         return telemetry.RecordDispatchError(ctx, err, stderr)  // records on active span, increments operations + errors counters, writes stderr two-liner, returns the mapped exit code
     }
     defer s.Close()
     s = telemetry.WrapStore(s)  // no-op until Task 12.4 lights up the decorator
     from, err := s.CurrentSchemaVersion(ctx)
     if err != nil {
         return telemetry.RecordDispatchError(ctx, err, stderr)
     }
     switch {
     case from < store.SupportedSchemaVersion:
         // parentCtx — NOT ctx — so quest.db.migrate is a sibling of the command span.
         migCtx, end := telemetry.MigrateSpan(parentCtx, from, store.SupportedSchemaVersion)
         applied, err := store.Migrate(migCtx, s)
         end(applied, err)
         if err != nil {
             return telemetry.RecordDispatchError(ctx, err, stderr)
         }
     case from > store.SupportedSchemaVersion:
         return telemetry.RecordDispatchError(ctx, errors.NewSchemaTooNew(from, store.SupportedSchemaVersion), stderr)
     }
     ```
     `quest.db.migrate` is **emitted only when the stored schema version lags** the binary (`from < SupportedSchemaVersion`). Per `OTEL.md` §4.1 / §8.8, the migrate span is a sibling of the command span, emitted only when a real migration is about to run — gating here preserves that "one migration = one span + one metric" contract and prevents every workspace-bound command from emitting a no-op `applied_count=0` span. When the stored version equals the supported version, skip both `MigrateSpan` and `store.Migrate` entirely. When it exceeds the supported version, fail with `ErrGeneral` (exit 1) — this is the forward-only schema-check described in `quest-spec.md` §Storage. The migrate span starts from `parentCtx` (the inbound `TRACEPARENT`), not from the command span. `WrapStore` is the single construction-site seam. Errors from `store.Open` and `CurrentSchemaVersion` map to `ErrGeneral` (exit 1) via `errorExit`; never discard them with `_`. `CurrentSchemaVersion` additionally maps `SQLITE_BUSY` to `ErrTransient` per Task 3.1. Because role denial short-circuits at step 3, a role-denied worker never reaches `store.Open` or migration — migration activity on the `dept.quest.schema.migrations` counter therefore correlates only to legitimate (non-role-denied) command invocations.
  6. If `descriptor.SuppressTelemetry`, call `descriptor.Handler(ctx, cfg, s, ...)` directly and return its exit code. Used today only by `version`, which suppresses span/metric emission per `OTEL.md` §4.2.
  7. Otherwise, call `telemetry.WrapCommand(ctx, descriptor.Name, fn)` where `fn` calls `descriptor.Handler(ctx, cfg, s, args, stdin, stdout, stderr)` and returns its error. `WrapCommand` is a **no-start / no-end middleware** per `OTEL.md` §8.2 — it picks up the already-active command span via `trace.SpanFromContext(ctx)`, applies the §4.4 three-step error pattern to the wrapped handler error, and increments `dept.quest.operations` / `dept.quest.errors`. It never starts a new span and never calls `span.End()` — `cli.Execute`'s step-2 `defer span.End()` owns the span's lifetime. Role-denied commands have already short-circuited at step 3 via `errorExit`, which performs the same counter increments — so role-denial observability is preserved without WrapCommand needing to know about it.
  8. **Panic recovery.** `cli.Execute` installs a top-level `defer recover()` that wraps the entire dispatch body (not inside `WrapCommand`). This covers the `SuppressTelemetry` short-circuit branch (step 6, `version`), the `WrapCommand` path (step 7), and any panic during steps 3–5 — a panic anywhere is translated into `ErrGeneral` wrapping `fmt.Errorf("panic: %v", r)`, logged at ERROR, and recorded on whatever command span is active (non-recording for `version`). Panic slog record shape: `err` field = truncated panic message (256 chars, via `telemetry.Truncate` for UTF-8 safety — matches `OBSERVABILITY.md` §Standard Field Names); `stack` field = captured via `runtime/debug.Stack()` truncated to 2048 bytes. Placing recovery at the dispatcher level (rather than inside `WrapCommand`) keeps the "every quest invocation produces a slog record on panic" invariant intact regardless of descriptor flags. `WrapCommand` stays focused on the three-step error pattern for handler-returned errors.
- **Handler boundary bookends.** `cli.Execute` emits `slog.DebugContext(ctx, "quest command start", attrs...)` after step 2 and `slog.DebugContext(ctx, "quest command complete", ...)` immediately before returning. Build attrs conditionally: always include `"command"` and `"agent.role"`; only include `"dept.task.id"` / `"dept.session.id"` when the resolved value is non-empty, matching `OBSERVABILITY.md` §Standard Field Names (omit-if-empty semantics). The complete bookend also adds `exit_code` and `duration_ms`. Handlers stay silent on boundaries; the dispatcher owns both bookend log records per `OBSERVABILITY.md` §Boundary Logging. Moving the bookends into the dispatcher keeps the call out of twelve handler files and guarantees consistent keys.
- Centralizing workspace validation, migration, store wrapping, the role gate, and the command span in dispatch keeps all cross-command security/observability boundaries in one place and matches `OTEL.md`'s hierarchy.
- **Dispatch-error helper lives in `internal/telemetry/`, not `internal/cli/`.** The single place pre-handler errors are recorded and converted to exit codes is `telemetry.RecordDispatchError(ctx context.Context, err error, stderr io.Writer) int` (Task 2.3 stub list, Task 12.5 implementation). The function pulls the active command span via `trace.SpanFromContext(ctx)`, calls `RecordHandlerError` (which applies the three-step error pattern + the C1 `quest.error.class` / `quest.error.retryable` / `quest.exit_code` attribute set + increments `dept.quest.errors{error_class}`), increments `dept.quest.operations{status=error}`, emits `slog.ErrorContext(ctx, "internal error", "err", telemetry.Truncate(err.Error(), 256), "class", errors.Class(err), "origin", "dispatch")` per `OTEL.md` §3.2 (the canonical ERROR-level message is shared with handler-level panics; the `origin="dispatch"` attribute distinguishes the two sources per the L9 decision), calls `errors.EmitStderr(err, stderr)`, and returns `errors.ExitCode(err)`. The previous draft kept this as a closure inside `internal/cli/` named `errorExit`, but that closure body referenced `trace.Span` and `codes.Error` directly — which would break the `OTEL.md` §10.1 import boundary (the grep tripwire `grep -rn 'go.opentelemetry.io' internal/ cmd/` admits matches only under `internal/telemetry/`). Relocating the helper to `internal/telemetry/` keeps the boundary intact and admits the explicit `stderr io.Writer` parameter — the previous closure body referenced a free `stderr` variable not present in its signature, which would not compile. Called from every early-return in the dispatch sequence (steps 3, 4, 5). Role-denied invocations (step 3) and workspace-failure invocations (steps 4, 5) therefore produce the same observability as a failed handler run: command span closed with the error recorded plus the C1 attributes set, `dept.quest.operations{status=error}` + `dept.quest.errors{error_class=<class>}` both incremented. Handlers themselves return errors unchanged — `WrapCommand` calls `telemetry.RecordHandlerError` to apply the same three-step + C1-attribute pattern on the already-open command span and emits its own `dept.quest.operations` increment (status=ok on success, status=error on handler-returned error).
- **Per-command flag parsing + `--help`.** Each handler constructs its own `flag.FlagSet` (`flag.NewFlagSet(descriptor.Name, flag.ContinueOnError)`), which makes `-h` and `--help` work natively — the standard library prints usage and returns `flag.ErrHelp`, which the handler translates to exit 0 (help is a success outcome). `quest` with no subcommand or `quest --help` prints the dispatcher's banner and exits 0. The `--help` text is not a contract (spec is silent); tests only assert it exists and exits 0. **`--help` does not short-circuit workspace / role checks.** A worker running `quest create --help` outside a workspace receives exit 2 (no workspace), and a worker running `quest create --help` inside a workspace receives exit 6 (role denied) — both are intentional. Help is gated by the same preconditions as running the command; a worker asking for help on a command they cannot run would still be blocked at the role gate a moment later. This departs from common CLI convention (git / kubectl / docker all short-circuit `--help`); tracked in *Deliberate deviations from spec* below with rationale.
- **Worker vs planner usage banner.** Workers see `{show, accept, update, complete, fail, init, version}`. `init` and `version` are always listed regardless of role per spec §System & Info Commands — a worker outside a workspace needs the `init` hint to bootstrap. Planners (any role listed in `.quest/config.toml` `elevated_roles`) see every entry in the descriptor inventory. The banner on unknown-command errors and on `quest --help` is filtered by the caller's resolved role.

**Tests:** Layer 4 CLI tests:

- Global flags accepted before and after the command name.
- Unknown command → exit 2 with `quest: usage_error: ...`.
- Worker role invoking `quest create` → exit 6 with `quest: role_denied: ...`.
- No workspace → exit 2 with the required message; `quest version` still works.
- `quest version` emits no span and no operation-counter increment (asserted via in-memory exporter; see Task 13.1 `TestVersionOutputShape`).
- `config.Validate` failure produces a command span with `class=usage_error` and increments `dept.quest.errors{error_class=usage_error}` — pre-handler errors feed the errors counter because the command span opens at step 2.
- A handler that panics produces a command span with `class=general_failure`, an ERROR slog record with a stack, and exit 1 — verifies the panic-recovery layer.

**Done when:** a structural end-to-end path (no real handlers yet) returns the correct exit codes for the happy and error cases above, and the span hierarchy (migration sibling + gate child) matches `OTEL.md` §4.1.

---

### Task 4.3 — Output renderer (`internal/output/`)

**Deliverable:** `output.Emit(w io.Writer, format string, value any) error`, `output.EmitJSONL[T any](w io.Writer, values []T) error` (slice-based form for the bounded uniform case), `output.NewJSONLEncoder(w io.Writer) *JSONLEncoder` with `(*JSONLEncoder).Encode(v any) error` (incremental form for streaming heterogeneous records), `output.OrderedRow` for dynamic-column rows, helpers for table rendering (`output.Table`) and tree rendering (`output.Tree`). The slice form handles `quest batch`'s ref→id stdout map (homogeneous `[]RefIDPair`); the encoder form handles the batch error stream where each line carries a different field set per error code (`duplicate_ref` → `first_line`; `cycle` → `cycle`; etc.). The slice form internally wraps the encoder for the common case so the two emitters cannot drift on quoting / trailing-newline / UTF-8.

**Spec anchors:** `quest-spec.md` §Output & Error Conventions (flat JSON, `null`/`[]`/`{}` never omitted), `STANDARDS.md` §CLI Surface Versioning (JSON is a contract).

**Implementation notes:**

- `json` mode: `json.NewEncoder(w).SetIndent("", "")` — compact, one final newline. The pretty-printed examples in `quest-spec.md` are for readability only; agents parse compact output.
- `text` mode column behavior: fixed default widths when `w` is not a TTY; auto-size to terminal width when it is. Use `golang.org/x/term` (or `x/sys/unix` directly) to query width; fall back to 80 columns when detection fails.
- Table truncation uses a trailing `...` per `quest-spec.md` §Text-mode formatting. Never split multi-byte runes — walk back to a rune boundary.
- **Nullable field pattern.** Optional string fields on task structs are declared as `*string` in Go; `encoding/json` already serializes `nil *string` as `null` and a populated pointer as a quoted string. Do not add a `output.NullString` helper — it was considered and dropped as redundant. The only responsibility of `internal/output/` around nulls is to *not* flatten the pointer.
- **Ordered-row emission for dynamic-column output.** `quest list --columns` determines field set and order at runtime, so a struct-per-command approach does not apply. Provide `output.OrderedRow` with signature `type OrderedRow struct { Columns []string; Values map[string]any }` and a `MarshalJSON` method that iterates `Columns` and emits `{"k1":v1,"k2":v2,...}` with keys in the supplied order. `json.Marshal` on a `map[string]any` sorts keys alphabetically — using `OrderedRow` is the only way to preserve `--columns` order per spec §`quest list` (row shape rules). Task 10.2 builds one `OrderedRow` per result row; `output.Emit` then writes the array.
- Provide `testutil.AssertSchema(t *testing.T, got []byte, required []string)` in `internal/testutil/` for contract tests (used heavily in Phase 6+). It lives in `testutil` because it is a test-side helper; the package prefix matches the import path.

**Tests:** Layer 1:

- Null vs empty: a `*string` nil-pointer emits `null`, an empty slice emits `[]`, an empty map emits `{}`.
- Truncation: multi-byte safety on a UTF-8 table cell that crosses the width boundary.
- JSONL: writer sees one trailing newline per record.

**Done when:** every task-JSON contract test in Phase 6 can call `output.Emit` and round-trip through `json.Unmarshal` cleanly.

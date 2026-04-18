# Quest Implementation Plan

**Audience:** coding agents implementing quest from scratch.
**Source docs:** `quest-spec.md` (behavioral contract), `STANDARDS.md`, `OBSERVABILITY.md`, `OTEL.md`, `TESTING.md`, `AGENTS.md`.

When this plan and the spec disagree, the spec wins — update the plan.

---

## How to use this plan

Work the phases top-to-bottom. Within a phase, tasks are ordered by dependency; later tasks assume earlier ones are done. Every task names:

- **Deliverable** — what must exist on disk when the task is complete.
- **Spec anchors** — sections in `quest-spec.md` (and, where relevant, the standards docs) that govern the task. Re-read these before starting; they contain MUST/MUST NOT rules that override intuition.
- **Implementation notes** — concrete shape of the code: package, types, signatures, edge cases.
- **Tests** — the test layer(s) the task must land with, per `TESTING.md`.
- **Done when** — a checklist that converts the spec into observable outcomes.

Hard rules that apply to every task:

- `internal/config/` is the only package that reads env vars, flags, or `.quest/config.toml`. Every other package accepts config values as parameters. (`STANDARDS.md` Part 1.)
- `internal/telemetry/` is the only package that imports OTEL. Everything else calls `telemetry.RecordX` / `telemetry.CommandSpan`. (`OTEL.md` §8.1, §10.1.)
- Stdout is the data channel, stderr is the diagnostic channel. Never `fmt.Println` for diagnostics; never `fmt.Println` for command results. (`OBSERVABILITY.md` §Anti-Patterns 1-2.)
- Exit codes 1–7 come from `internal/errors.ExitCode(err)`. Never hardcode `os.Exit(5)` anywhere but `main`. (`OBSERVABILITY.md` §Error Handling.)
- No third-party test libraries; table-driven subtests; standard library only. (`TESTING.md` §Philosophy.)
- When a spec field or error code is unclear, stop and resolve it in the spec first. Do not invent behavior. (`AGENTS.md` §What not to do.)

---

## Spec-resolution prerequisites

The plan review surfaced open spec questions. Resolved questions (cancelled-update behavior, `update` precondition precedence, batch `parent` ref/id shape, `quest version` JSON shape, stderr trace-ID enrichment, module path, `CommandSpan` return shape, leaf status-transition atomicity, tag validation rules, enum-filter OR semantics across `--status`/`--role`/`--tier`/`--type`/`--parent`, dependency-flag repeatability, `quest init`/`move`/`cancel`/`reset` JSON shapes, `quest list` JSON row shape, `@file` size limit and error formats, write-command output shapes for `accept`/`complete`/`fail`/`create`/`update`/`link`/`unlink`/`tag`/`untag`, empty-value rejection on `--role`/`--handoff`/`--title`/`--description`/`--context`/`--acceptance-criteria`/`--note`, `--type` transition rejection when `caused-by`/`discovered-from` links exist, `quest graph` requires an explicit ID, `--color` flag dropped from v0.1) are now specified in the relevant anchor doc and the corresponding plan task. No entries are outstanding.

Flag any additional ambiguities discovered during implementation here before coding around them.

### Cross-cutting rules resolved at review time

These are the global rules that multiple tasks below depend on. Re-confirming them up front so each task can reference without restating:

- **Timestamps are second-precision RFC3339 UTC** -- `time.Now().UTC().Format(time.RFC3339)`. Applies to every `started_at`/`completed_at`/`handoff_written_at`, every `history.timestamp`, every `notes.timestamp`, and PR `added_at`. See `quest-spec.md` §Output & Error Conventions.
- **Nullable TEXT columns** (`owner_session`, `handoff`, `handoff_session`, `handoff_written_at`, `role`, `tier`, `acceptance_criteria`, `parent`, `debrief`, `history.role`, `history.session`, etc.) are written with `sql.NullString{}` when the source Go string is empty. `quest show` output emits JSON `null` for unset values, never `""`. Direct SQLite inspection sees `NULL`, not `''`. This rule is enforced at the write path (inside handlers and `store.AppendHistory`), not by a read-side coercion layer.
- **`@-` is single-use per invocation.** `input.Resolve` counts the number of `@-` arguments parsed and rejects the second one with exit 2: `"stdin already consumed by <first-flag>; at most one @- per invocation"`. Applies to `--debrief`, `--note`, `--handoff`, `--description`, `--context`, `--reason`, `--acceptance-criteria` -- any flag in spec §Input Conventions' `@file`-supporting list.
- **`--color` is not a flag in v0.1.** Global flag parsing in Task 4.2 parses `--format` and `--log-level` only. `config.Flags` has two fields. Text-mode rendering in Task 4.3 emits plain text; TTY detection still informs column widths but not color.

---

## Phase 0 — Repository bootstrap

### Task 0.1 — Initialize the Go module and directory skeleton

**Deliverable:** `go.mod`, `go.sum`, `cmd/quest/main.go`, an empty `internal/` tree matching the package map below, `Makefile`, `CHANGELOG.md`, `.gitignore`.

**Spec anchors:** `AGENTS.md` §Folder structure, `STANDARDS.md` §Changelog, `OTEL.md` §10.1.

**Implementation notes:**

- Module path: `github.com/mocky/quest`. Record this in `go.mod` and in every test import; do not use a placeholder. The path is lowercase to match Go community convention and to avoid case-sensitivity issues on macOS APFS / Windows NTFS filesystems.
- Go version: `1.21` or later. Picks up `slog` / `log/slog` stability; nothing in the plan depends on Go 1.23-only features.
- Create these empty-but-committed package directories with one `doc.go` per package:
  ```
  cmd/quest/                  main + run: load config, init logging, telemetry.Setup, ExtractTraceFromConfig, defer shutdown, call cli.Execute. Nothing else — no flag parsing, no dispatch, no handlers.
  internal/cli/               global flag parsing, command dispatcher, role gate, WrapCommand, per-command flag parsing
  internal/command/           flat package (one file per command handler — accept.go, create.go, ...); contract tests in internal/command/contract_test.go (see Task 13.1 for the flat layout)
  internal/config/            the single config package (see Phase 1)
  internal/logging/           slog setup, fan-out handler shell
  internal/errors/            exit-code + error-class mapping
  internal/ids/               prefix validation, short-ID generation
  internal/input/             `@file` / `@-` / stdin resolver (mirrors internal/output/; see Task 6.3)
  internal/store/             SQLite layer, migrations, tasks/history/deps/tags
  internal/batch/             quest batch four-phase validator
  internal/output/            json/text rendering helpers
  internal/export/            quest export layout generator
  internal/telemetry/         OTEL package (added as a no-op shell in Task 2.3, filled in Task 12.1)
  internal/testutil/          shared test helpers. Planned surface: NewWorkspace(t), NewStore(t), NewFakeStore(t) (in-memory Store fake for unit tests without SQLite), SeedTask(t, store, Task), AssertExitCode(t, got, want int), AssertErrorClass(t, err, wantClass string), AssertJSONKeyOrder(t, got []byte, want []string), AssertSchema(t, got []byte, required []string), NewCapturingTracer(t) / NewCapturingMeter(t) / NewCapturingLogger(t) (OTEL test providers via tracetest / metrictest).
  internal/buildinfo/         version string wired with -ldflags
  ```
- Each `doc.go` is a single package comment (`// Package <name> ...`) of one paragraph: summarize the package's role, state its import-direction constraints (e.g., "imports no other internal packages except X"), name the key exported type(s), and cite the governing spec anchor. Keep it under ~120 words — these render in `go doc` and serve as the first orientation a new contributor gets for the package.
- `cmd/quest/main.go` must be a two-function shell per `OTEL.md` §7.1:
  ```go
  func main() { os.Exit(run()) }
  func run() int { ... }
  ```
  No handler ever calls `os.Exit`.
- `Makefile` targets. Declare `MODULE` and `VERSION` at the top of the file so every target sees them:
  ```make
  MODULE  := github.com/mocky/quest
  VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
  ```
  Targets:
  - `build` — `go build -ldflags "-X $(MODULE)/internal/buildinfo.Version=$(VERSION)" -o quest ./cmd/quest`
  - `test` — `go test ./...`
  - `test-all` — `go test -race -tags integration -count=1 ./...`
  - `cover` — `go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out`
  - `lint` — `go vet ./...` followed by `gofmt -l .` (the second command exits non-zero if any file needs reformatting, which is the intended behavior)
  - `ci` — `go test -race -count=1 -tags integration -coverprofile=coverage.out ./...`, matching `TESTING.md` §CI Expectations
- `go.mod` dependencies: pin `modernc.org/sqlite v1.28.0` or newer — the version must support the `_txlock=immediate` DSN parameter (stable since `v1.14.6`, Feb 2022) and expose `sqlite.RegisterConnectionHook` (confirmed first exported in v1.28.0; older v1.20.x–v1.27.x exported the `RegisterConnectHook` spelling without the `-ion-`). For the v1.28.0 pin, use `RegisterConnectionHook`; if a future downgrade is considered, fall back to `RegisterConnectHook` and re-verify. The DSN carries only `?_txlock=immediate` (no pragma params); genuine SQLite pragmas (`journal_mode`, `busy_timeout`, `foreign_keys`, `defer_foreign_keys`) are issued post-open via the connect hook. See Task 3.1 for the full rationale. Also pin `github.com/BurntSushi/toml` at its current release and `golang.org/x/term` (used by `internal/output/` for TTY width detection in Task 4.3).
- **`_txlock=immediate` is silently ignored on read-only transactions.** Since commit `abc96aa` (shipped in `modernc.org/sqlite` v1.20.3, Jan 2023), opening a transaction via `db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})` issues a plain `BEGIN` (deferred) regardless of the DSN's `_txlock` value. Quest as planned does not use `ReadOnly: true` — every transaction goes through `BeginImmediate` which calls `BeginTx(ctx, nil)` — so the contract holds. Future contributors must NOT introduce a `ReadOnly: true` transaction without separately documenting that it bypasses the write-serialization contract. Task 3.1 forbids exposing a read-only transaction primitive on the `Store` interface; this is the rationale.
- **Verify `_txlock=immediate` support before Phase 3 begins.** Before Task 3.1 lands, run a one-shot verification: open a DB with `?_txlock=immediate`, hold the write lock from one connection, assert a second `BeginTx(ctx, nil)` blocks until the first releases (not fails immediately). Then, with the lock still held, attempt a second `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})` and assert it does **not** block (proving the read-only caveat is real and any future code that opens a read-only tx silently bypasses the write-lock contract). Also smoke-test that the pinned driver version exports `RegisterConnectionHook` (v1.28.0+) — if it instead exports the older `RegisterConnectHook` spelling, record which one is available so Task 3.1's connect-hook installation matches. Commit all three verification results to `internal/store/testdata/_txlock_verify.md` so future driver upgrades can re-run them. If the pinned driver version does not recognize `_txlock=immediate`, every `BeginImmediate` silently degrades to `BEGIN` (deferred) and the exit-code-7 contract collapses — the failure mode is catastrophic, so verify the foundational assumption before building on it.
- `CHANGELOG.md` seed content per `STANDARDS.md` §Changelog. File contents literally:
  ```markdown
  # Changelog

  ## [Unreleased]

  ### Added
  ### Changed
  ### Deprecated
  ### Removed
  ### Fixed
  ### Schema
  ```
  The six `###` sections stay even when empty so later edits only need to add bullets. The "Follows Keep a Changelog" reference lives in `STANDARDS.md` §Changelog (not in the seed file) to keep the seed minimal.
- `.gitignore`: `quest` binary, `quest-export/`, `.quest/quest.db*`, `coverage.out`.

**Tests:** none yet; verify `go build ./...` and `go test ./...` succeed on the empty scaffolding.

**Done when:** `make build` produces a `quest` binary that runs and prints nothing (or an explicit "not implemented" with exit 1), `make test` passes, every listed directory exists and has a `doc.go`.

---

### Task 0.2 — Wire the `quest version` command (minimal end-to-end path)

**Deliverable:** `quest version` prints the build version and exits 0. `internal/buildinfo.Version` is a package-level string set via `-ldflags -X`.

**Spec anchors:** `quest-spec.md` §`quest version`, `STANDARDS.md` §Versioning Scheme, `OTEL.md` §4.2 (version is span-suppressed).

**Implementation notes:**

- `internal/buildinfo/buildinfo.go` defines `var Version = "dev"`.
- `Makefile` `build` target injects the version: `go build -ldflags "-X $(MODULE)/internal/buildinfo.Version=$(VERSION)" -o quest ./cmd/quest`, where `VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`.
- `quest version` output matches spec §`quest version`: `--format json` (default) emits `{"version": "..."}` with a single always-present `version` field; `--format text` emits the bare version string followed by a newline. Pin the JSON shape in the Task 13.1 contract test (`TestVersionOutputShape`).
- Version command runs the standard startup flow. Concretely `main.run()` (`OTEL.md` §7.1) performs:
  1. Parse global flags (`--format`, `--log-level`) from `os.Args[1:]` into `config.Flags` via `flags, remainingArgs := cli.ParseGlobals(os.Args[1:])`. `main.run()` owns flag parsing so the logger / telemetry are built with the resolved values; `cli.Execute` does not re-parse.
  2. `cfg := config.Load(flags)` — infallible.
  3. `logger := logging.Setup(cfg.Log)` then `slog.SetDefault(logger)` (no OTEL bridge yet; the bridge is added after `telemetry.Setup`).
  4. Call `telemetry.Setup` with a fully-populated `telemetry.Config` including `CaptureContent` from `cfg.Telemetry.CaptureContent`:
     ```go
     bridge, otelShutdown, _ := telemetry.Setup(ctx, telemetry.Config{
         ServiceName:    "quest-cli",
         ServiceVersion: buildinfo.Version,
         AgentRole:      cfg.Agent.Role,
         AgentTask:      cfg.Agent.Task,
         AgentSession:   cfg.Agent.Session,
         CaptureContent: cfg.Telemetry.CaptureContent,
     })
     ```
     Errors from `Setup` are logged and ignored (telemetry is a secondary output; `Setup` always returns usable providers). `defer otelShutdown(shutdownCtx)` with a 5-second timeout. Omitting `CaptureContent` here would silently make `OTEL_GENAI_CAPTURE_CONTENT=true` a no-op at runtime; every field on `telemetry.Config` must be set explicitly.
  5. `logger = logging.Setup(cfg.Log, bridge)`; re-install as default — this is the fan-out that adds the otelslog handler.
  6. `ctx = telemetry.ExtractTraceFromConfig(ctx, cfg.Agent.TraceParent, cfg.Agent.TraceState)` — the **one and only** place in the code that extracts the inbound W3C trace context. Must happen before any child span is created; vigil→quest trace correlation depends on it. `cli.Execute` does not re-extract.
  7. `return cli.Execute(ctx, cfg, remainingArgs, os.Stdin, os.Stdout, os.Stderr)`. `cli.Execute` takes the resolved `cfg` directly — no second `config.Load`.
  `version` works with no workspace because its descriptor has `RequiresWorkspace=false`; the dispatcher skips `cfg.Validate`, store open, and migrate.
- Per `OTEL.md` §4.2, `version` is explicitly suppressed from span and metric emission — no `CommandSpan` call, no operation counter increment. Suppression happens at dispatch time inside `cli.Execute`, not by skipping setup.
- **Accepted trade-off** (see `OTEL.md` §4.2 "Excluded from span instrumentation" > version): when OTEL is *enabled* (the non-default case), `quest version` pays the cost of `telemetry.Setup` and `Shutdown` — exporter construction, batch-processor goroutines, W3C propagator install — even though the command emits nothing. This is an intentional simplification; revisit only if a scripted consumer materially cares about version latency with OTEL enabled.
- **Shutdown wiring.** `main.run()` defers `otelShutdown` with a 5-second timeout context per `OTEL.md` §7.1 (`context.WithTimeout(..., 5*time.Second)`). This upper-bounds the flush on exit so a misconfigured collector cannot hang the CLI. The same pattern is mirrored by `telemetry.Setup`'s returned shutdown function in Task 12.1.

**Tests:** Layer 4 CLI test. Build the binary in `TestMain`, run `quest version`, assert exit 0 and that stdout parses as JSON with a `version` key.

**Done when:** `quest version` returns valid JSON in default mode and the bare version in `--format text`, with exit 0.

---

## Phase 1 — Configuration

### Task 1.1 — `internal/config/`: workspace discovery + file parse

**Deliverable:** `config.DiscoverRoot(startDir string) (string, error)` that walks up from `startDir` until it finds a `.quest/` directory, returning the absolute path. Returns a sentinel `ErrNoWorkspace` when no `.quest/` is found before hitting the root.

**Spec anchors:** `quest-spec.md` §Tool Identity (walk-up discovery), `quest-spec.md` §Storage (nested workspaces unsupported), `STANDARDS.md` §The Config Package.

**Implementation notes:**

- Stop at the first `.quest/` encountered. Do not warn if a second `.quest/` exists higher up — walk-up has already stopped.
- Use `os.Stat` on the directory; tolerate permission errors by returning them (exit 1 path), not by skipping.
- Add a companion `config.ReadFile(root string) (FileConfig, error)` that reads `.quest/config.toml`. Parse with `github.com/BurntSushi/toml` (standard library does not parse TOML). Unknown fields are kept but `slog.Warn`'d per `STANDARDS.md`.
- `FileConfig` fields per `STANDARDS.md` §Config File: `IDPrefix string`, `ElevatedRoles []string`. Keep room for forward-compat growth.

**Tests:** Layer 1 unit tests in `internal/config/config_test.go`:

- Discovers at CWD, one level up, many levels up.
- Stops at the first hit (creates nested `.quest/` and asserts the inner one wins).
- Returns `ErrNoWorkspace` when no marker is found.
- Parse: valid file, missing file, malformed TOML, unknown field (warn, not error), empty file.

**Done when:** the table-driven tests above pass, and the package has no `os.Getenv` calls yet.

---

### Task 1.2 — Prefix validator (`internal/ids/prefix.go`)

**Deliverable:** `ids.ValidatePrefix(prefix string) error` that returns a non-nil error with a message citing the failing rule, exported at package scope.

**Spec anchors:** `quest-spec.md` §Prefix validation. Specifically: `^[a-z][a-z0-9]{1,7}$`, length 2–8, must start with a letter, reserved values `ref`.

**Implementation notes:**

- Use a compiled `regexp.MustCompile` at package init for the format match.
- Reserved values are a set (`map[string]struct{}` or a switch). Today only `ref`; accept that the list may grow.
- Error messages must name the specific rule: `"must be 2-8 characters"`, `"must start with a letter"`, `"lowercase letters and digits only"`, `"reserved prefix"`. The `TESTING.md` sample test asserts on these substrings.

**Tests:** Layer 1 table-driven tests per the `TESTING.md` §Table-Driven Tests example. At minimum the 11 cases shown there.

**Done when:** every case from the spec's Prefix validation section is covered by a named subtest, and tests pass.

---

### Task 1.3 — `internal/config/`: env/flag resolution + `Load` + `Validate`

**Deliverable:** `config.Config` struct, `config.Flags` struct, `config.Load(flags Flags) Config`, `(Config).Validate() error`, `config.IsElevated(role string, elevated []string) bool`.

**Spec anchors:** `STANDARDS.md` Part 1 (full section, especially §Config Struct, §Validation, §Defaults), `quest-spec.md` §Role Gating (env var names), `OBSERVABILITY.md` §Correlation Identifiers, `OTEL.md` §6.2 and §7.1 (telemetry consumes resolved identity strings, never env vars).

**Implementation notes:**

- Shape exactly as laid out in `STANDARDS.md` §Config Struct: `Workspace`, `Agent`, `Log`, `Telemetry`, `Output`. `WorkspaceConfig` carries a `DBPath` field populated as `filepath.Join(Root, ".quest/quest.db")` at load time — downstream packages (store, init) take this as a parameter rather than recomputing it. `OutputConfig` carries a `Format string` field populated from `flags.Format` (default `"json"`); handlers read `cfg.Output.Format` to decide between JSON and text rendering. Format is rendering, not logging, so it gets its own section rather than riding on `LogConfig`.
- `config.Flags` is a two-field struct matching the two global flags that survive after the `--color` drop:
  ```go
  type Flags struct {
      Format   string // --format json|text; empty means default ("json")
      LogLevel string // --log-level debug|info|warn|error; empty means default ("warn")
  }
  ```
  No `Color` field. Task 4.2 parses exactly these two globals.
- `Agent` struct fields: `Role`, `Task`, `Session`, `TraceParent`, `TraceState`. All five are populated in this package from `AGENT_ROLE` / `AGENT_TASK` / `AGENT_SESSION` / `TRACEPARENT` / `TRACESTATE` respectively. Empty string is a valid state — do not substitute anything. Downstream packages (telemetry, role gating, history, stderr trace-ID enrichment) consume the typed fields; none of them read env vars directly. This honors the "one package reads env" invariant in `STANDARDS.md` Part 1 and is the telemetry-facing contract in `OTEL.md` §6.2 / §7.1.
- Precedence: `flag > env var > .quest/config.toml > default`. Implement with a `firstNonEmpty` helper.
- `Log.Level` is sourced from `QUEST_LOG_LEVEL` (default `"warn"`); `Log.OTELLevel` is sourced from `QUEST_LOG_OTEL_LEVEL` (default `"info"`). These are the only two logging knobs per `OBSERVABILITY.md` §Logger Setup. Both env vars are read inside `config.Load`; no other package reads them. Adding the OTEL env var later would be a breaking change, so support it from day one.
- `Telemetry.CaptureContent` is parsed with `strconv.ParseBool` on `OTEL_GENAI_CAPTURE_CONTENT`. An unparseable value (e.g., `yes`, `on`, `1.0`) does **not** fail startup: emit `slog.Warn("invalid OTEL_GENAI_CAPTURE_CONTENT", "value", raw)` once and default `CaptureContent` to `false`. Content capture is strictly opt-in, so silent-off-on-malformed is the safe default; the warn makes the operator error visible.
- **`Load` is tolerant; `Validate` is explicit.** `Load(flags) Config` always returns a populated config struct (never an error). `(Config).Validate() error` is a method (matches `STANDARDS.md` §Validation) that collects every validation error into a single returned multi-line string (`<source>: <what's wrong>`) and is called by the dispatcher for commands that require a workspace — `quest init` and `quest version` run without calling `Validate` because they must work in directories that have no `.quest/` (for `init`) or where `IDPrefix` is absent (for `version`). This is the resolution for the version-command startup conflict: the validation choice belongs to the caller, not the loader. See Task 4.2 dispatch sequence.
- I/O errors reading `.quest/config.toml` other than "file missing" (permission denied on an existing file, malformed TOML, read error mid-walk-up) are logged once via `slog.Warn` naming the path and the raw OS error, then `Load` populates defaults as if the file were absent. This keeps `Load` infallible; `Validate` surfaces the resulting missing fields when the dispatcher calls it. Matches `STANDARDS.md` §Defaults.
- This package is the only reader of `.quest/config.toml`. `ReadFile` from Task 1.1 is called from `Load`; if the file is missing (`quest init` hasn't run yet), `Load` populates defaults and `Validate` — if later called — reports the missing fields.
- Export one helper: `config.IsElevated(role string, elevated []string) bool` — used by the role gate in Phase 4. Keep the gate decision centralized.

**Tests:** Layer 1 tests in `config_test.go`:

- Env var resolution using `t.Setenv` (never `os.Setenv`), including `TRACEPARENT` / `TRACESTATE` landing on `cfg.Agent`.
- Flag override beats env var beats file default.
- `Load` in a workspaceless directory succeeds; `Validate` on that config returns a non-nil error naming the missing `IDPrefix` and workspace.
- `Validate()` collects multiple errors; message contains all offending sources.
- `IsElevated`: empty role, role present in list, role absent, list empty.
- `OTEL_GENAI_CAPTURE_CONTENT=yes` → `CaptureContent=false` + a single slog warn record captured via a test `slog.Handler`.

**Done when:** tests pass, and a grep for `os.Getenv` across the codebase finds only `internal/config/`.

---

## Phase 2 — Logging and errors

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

---

## Phase 3 — Storage foundation

### Task 3.1 — `internal/store/`: SQLite open, WAL, busy_timeout

**Deliverable:** `store.Open(path string) (Store, error)` returning the `Store` interface declared in Task 3.3 (backed by an unexported `*sqliteStore`), `(Store).Close() error`, the `store.TxKind` enum matching `OTEL.md` §4.3 / §5.3 (`accept`, `create`, `complete`, `fail`, `reset`, `cancel`, `cancel_recursive`, `move`, `batch_create`, `link`, `unlink`, `tag`, `untag`, `update`), and the `store.TxOutcome` enum (`TxCommitted`, `TxRolledBackPrecondition`, `TxRolledBackError`). Migrations deliberately do not use the `TxKind` enum (see Task 3.2) — they call `db.BeginTx` directly so `quest.db.migrate` is the only span on that code path, and the `dept.quest.store.tx.duration{tx_kind}` histogram is not polluted by migrations that run orders of magnitude slower than ordinary writes.

**Spec anchors:** `quest-spec.md` §Storage (WAL mode, `PRAGMA busy_timeout = 5000`, serialized writes), `OTEL.md` §4.3 / §5.3 (`tx_kind` enum).

**Implementation notes:**

- Driver: `modernc.org/sqlite` pinned at the version chosen in Task 0.1 (pure Go, no CGo — preferred for CLI portability). Import as `_ "modernc.org/sqlite"`.
- **DSN: `file:<path>?_txlock=immediate`.** `_txlock` is a driver-level parameter (not a SQLite pragma) recognized by `modernc.org/sqlite`; setting it causes `db.BeginTx` to issue `BEGIN IMMEDIATE` instead of the default `BEGIN` (deferred). This is the only correct path to an IMMEDIATE transaction through `database/sql` — issuing `Exec("BEGIN IMMEDIATE")` on a `*sql.Tx` returned by `BeginTx` fails with "cannot start a transaction within a transaction" because `BeginTx` has already opened its own `BEGIN`. `_txlock` must be on the DSN (driver-level parameters cannot be set post-open); genuine pragmas (`journal_mode`, `busy_timeout`, `foreign_keys`, `defer_foreign_keys`) are still issued post-open via the connect hook. **Do not add `cache=shared`** — WAL gives adequate concurrent-read behavior without shared cache's mutex complexity; this exclusion is documented so future performance debugging does not guess at it. **Read-only-transaction caveat:** since `modernc.org/sqlite` v1.20.3 (Jan 2023), `_txlock=immediate` is silently skipped for transactions opened with `sql.TxOptions{ReadOnly: true}` — those issue plain `BEGIN` (deferred). The `Store` interface (Task 3.3) deliberately does NOT expose `BeginTx` or any read-only transaction opener; the only public transaction primitive is `BeginImmediate`, which uses `BeginTx(ctx, nil)`. Do not add a read-only transaction method without separately documenting that it bypasses the write-serialization and exit-code-7 contracts. The Task 0.1 verification fixture covers this caveat (see `_txlock_verify.md`); a Layer 3 test `TestReadOnlyTxBypassesTxLock` opens a read-only `BeginTx` via direct SQL inside the test (bypassing the production API) and asserts the second transaction does not block, proving the caveat is real and will trip if the invariant changes.
- **Per-connection pragmas via a connect hook.** `journal_mode=WAL` is a *database-header* pragma (persistent); set it once post-open on the primary connection and assert the result equals `"wal"`. `busy_timeout` and `foreign_keys` are *per-connection* pragmas and default to 0 / OFF on every fresh connection Go's pool may open. Register the hook from a **package-level `init()`** in `internal/store/`: `modernc.org/sqlite`'s `RegisterConnectionHook` is a package-global mechanism, so calling it from `store.Open` would re-register the hook on every `Open` call (harmless but a slow leak — the driver would invoke N hooks per new connection). Installing in `func init()` guarantees exactly-once registration before any `store.Open` call. Issue `PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;` on every new connection (the `defer_foreign_keys` pragma is intentionally excluded — see below). In `modernc.org/sqlite` v1.28+ the canonical spelling is `RegisterConnectionHook`; on older driver versions that expose only `RegisterConnectHook` (no `-ion-`), use that name. If neither exists, issue the two pragmas on each `BeginImmediate` (one extra round-trip). Without the hook, a pool-allocated second writer would get `SQLITE_BUSY` immediately instead of waiting 5s, breaking the exit-code-7 contract. Layer 3 test: hold the write lock from one connection for 3s, assert a second writer blocks and then succeeds. Do not cap `MaxOpenConns` — WAL gives free concurrent reads; the connect hook makes the pool safe.
- **Do not issue `PRAGMA defer_foreign_keys` in the connect hook.** The pragma defaults to OFF in SQLite and automatically resets on every `COMMIT`/`ROLLBACK` — it is a per-transaction override that never persists across transactions, so a connect-hook value would always be a no-op. Per the H15 decision, **no quest handler issues `defer_foreign_keys` at all**: `quest move` (Task 8.3) relies on `ON UPDATE CASCADE` to rewrite every referencing row atomically within the triggering `UPDATE tasks` statement, which leaves no transient FK-violating state to defer. Keeping the connect hook minimal and the move handler free of the pragma prevents future readers from assuming it is load-bearing.
- **`Open` does not migrate.** It establishes the connection and applies pragmas; the schema check is a separate call (`store.Migrate`, Task 3.2) driven by the dispatcher (Task 4.2 step 5) so the migration span is a sibling of the command span per `OTEL.md` §4.1 / §8.8. `Open` is cheap and safe to call from any command-handler path without creating a migration span underneath the command span.
- `Store` is an interface (Task 3.3). `Open` returns the interface backed by `*sqliteStore`; the concrete type is unexported. This lets the `InstrumentedStore` decorator (Task 12.4) wrap the store without embedding a concrete `*Store`, and lets `internal/testutil/` substitute fakes. Define `Store` in `store.go` (alongside `Open`) or in its own `interface.go` — either works.
- `*sqliteStore` wraps `*sql.DB`. Do not cap `MaxOpenConns` — the driver already serializes writes at the SQLite layer, and readers are free under WAL.
- **`BeginImmediate` takes a `TxKind` and returns `*store.Tx`.** Signature:
  ```go
  BeginImmediate(ctx context.Context, kind TxKind) (*store.Tx, error)
  ```
  `store.Tx` (declared in `store/tx.go`) wraps an unexported `*sql.Tx` field and exposes the full public API:
  ```go
  func (tx *Tx) Commit() error
  func (tx *Tx) Rollback() error
  func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)   // auto-accumulates rows_affected
  func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)   // pass-through
  func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row           // pass-through
  func (tx *Tx) MarkOutcome(outcome TxOutcome)                                                      // handler hints outcome before close (TxCommitted / TxRolledBackPrecondition / TxRolledBackError)
  ```
  Internal fields: `kind TxKind` (for the decorator's `quest.store.tx` span attribute), `invokedAt time.Time` (captured when `BeginImmediate` is called), `startedAt time.Time` (captured after the underlying `BEGIN IMMEDIATE` returns — their delta is `quest.tx.lock_wait_ms`), `rowsAffected int64` (running sum maintained by the `ExecContext` wrapper), `outcome string` (set by `MarkOutcome`; defaulted by the hook to `committed` on successful commit or `rolled_back_error` on rollback), and optional `onCommit` / `onRollback` hook fields populated by the `InstrumentedStore` decorator in Task 12.4 (nil on the bare store). `Commit()` / `Rollback()` call the hook (if set) after the underlying method returns — that is how the decorator closes its `quest.store.tx` span without needing to override methods on a concrete type. The `rowsAffected` counter and `outcome` string are read by `onCommit` / `onRollback` to populate `quest.tx.rows_affected` and `quest.tx.outcome` on the span (Task 12.4).
  Concrete implementation, because `_txlock=immediate` is on the DSN: record `invokedAt = time.Now()`, call `db.BeginTx(ctx, nil)` (which issues `BEGIN IMMEDIATE`), record `startedAt = time.Now()`, wrap the `*sql.Tx` in `*store.Tx`, and return. No separate `Exec("BEGIN IMMEDIATE")` call. The timing fields are the single source of truth for lock-wait — the decorator reads them, never re-computes (see `OTEL.md` §8.4 and Task 12.4).
  **`ExecContext` is a thin wrapper that auto-accumulates `rows_affected`.** Body: call `inner.ExecContext(ctx, query, args...)`; if the returned `sql.Result` is non-nil, read `RowsAffected()` and add to `tx.rowsAffected` (skip the addition if `RowsAffected()` returns an error or `-1` — some drivers report `-1` for INSERT without `LastInsertId` interest). Return the `sql.Result` and error to the caller unchanged. This keeps accumulation invisible to handlers — no per-Exec `tx.AddRowsAffected(n)` call required. Handlers can opt out of counting via an `(tx *Tx).IgnoreRows()` helper for queries whose "rows returned" is not interesting (reserved for future; v0.1 always counts).
  `MarkOutcome(outcome string)` lets handlers override the default outcome classification for the span attribute: `rolled_back_precondition` (expected precondition check failed, e.g., parent not in `open`, non-terminal children on complete) versus `rolled_back_error` (unexpected failure). Precondition rollbacks surface exit code 5 but are not bugs; separating them keeps dashboards clean. **If `MarkOutcome` is not called, the hook auto-infers from the returned error** — `committed` on successful commit; otherwise `errors.Is(err, errors.ErrConflict)` / `errors.Is(err, errors.ErrNotFound)` / `errors.Is(err, errors.ErrPermission)` → `rolled_back_precondition`; any other error → `rolled_back_error`. Handlers therefore do not need to call `MarkOutcome` for the common precondition paths — returning the right typed error sentinel is enough. `MarkOutcome` stays available as an escape hatch for future error classes or bespoke tagging.
  Handlers call the explicit methods only: `tx.ExecContext`, `tx.QueryContext`, `tx.QueryRowContext`, `tx.MarkOutcome`, `tx.Commit`, `tx.Rollback`. Per-DML `quest.store.op` span events are explicitly not part of the design (`OTEL.md` §4.2 / §8.3 / §19) — the transaction-level `quest.store.tx` span plus the three named child spans (`quest.role.gate`, `quest.db.migrate`, `quest.batch.*` phase spans) are the full store-side instrumentation contract. Handlers therefore pass plain `ExecContext(ctx, sql, args...)` without op/target strings.
- **Per-transaction DEBUG bookends on bare `*store.Tx`.** Per `OBSERVABILITY.md` §Boundary Logging §Per-Transaction Boundaries, `*store.Tx` emits `slog.DebugContext` records at `BEGIN IMMEDIATE` acquire, commit, and rollback — `"BEGIN IMMEDIATE acquired" tx_kind=<kind> lock_wait_ms=<dur>`, `"tx committed" tx_kind=<kind> rows_affected=<n>`, `"tx rolled back" tx_kind=<kind> outcome=<outcome>`. These land on the bare `*store.Tx` (not the telemetry decorator) so they fire regardless of whether OTEL is configured — slog bookends and OTEL spans are independent signals. The records use the same `tx_kind` / `rows_affected` / `outcome` field names as the span attributes for cross-reference.
- **`(Store).CurrentSchemaVersion(ctx) (int, error)`** is a method on the `Store` interface (declared as a Phase-2 prerequisite per the Phase 2 implementation-order note) — used by the dispatcher (Task 4.2 step 5) to read the stored `meta.schema_version` before calling `telemetry.MigrateSpan(ctx, from, to)`. Returns 0 (not an error) when the `meta` table does not yet exist — see Task 3.2 for how migration 001 bootstraps it. **Error mapping:** `sqlite3 code 5 (SQLITE_BUSY)` → wraps `ErrTransient` (exit 7); any other driver error → wraps `ErrGeneral` (exit 1); the "meta table missing" sentinel is the only non-error zero return. The method lives on the interface so the `InstrumentedStore` decorator (Task 12.4) can wrap it and so test fakes can stub a specific version without hitting SQLite; the "meta missing" bootstrap is internal to `*sqliteStore`.

**Tests:** Layer 3 integration tests (tagged `//go:build integration`):

- Open creates the file and applies the pragmas (`SELECT ... FROM pragma_journal_mode`).
- `Open` on a fresh path does **not** create schema tables; schema setup is gated on `Migrate`.
- Concurrent readers do not block a writer (open two stores against the same DB file, run a long read and a write concurrently).

**Done when:** `store.Open` returns a live `Store` handle, `PRAGMA journal_mode` returns `wal`, and the schema is still absent until `Migrate` runs.

---

### Task 3.2 — Migration framework + schema v1

**Deliverable:** `internal/store/migrate.go` exporting `store.Migrate(ctx context.Context, s Store) (applied int, err error)`, `internal/store/migrations/001_initial.sql` with the full initial schema, and the `store.SupportedSchemaVersion` constant (a single integer — the schema version this binary supports). There is no separate `SchemaVersion` constant; the DB's current version is read at runtime via `(Store).CurrentSchemaVersion`, and the compile-time upper bound lives only in `SupportedSchemaVersion`.

**Spec anchors:** `quest-spec.md` §Storage (`schema_version` meta table, forward-only migrations, newer-than-supported refuses to run), `STANDARDS.md` §Schema Migration Rules, `OTEL.md` §4.1 / §8.8 (migration span is a sibling of the command span — drives where `Migrate` is called from in Task 4.2).

**Implementation notes:**

- Migrations are embedded via `//go:embed migrations/*.sql`. **Filename convention is `NNN_label.sql` with a three-digit zero-padded prefix** (`001_initial.sql`, `002_add_foo.sql`, ...). The runner does NOT rely on lexical iteration of the embed FS — instead, it parses the leading numeric prefix (everything before the first `_`) into an int, sorts the migrations numerically, and asserts no gaps between consecutive versions. The highest sorted version must equal `SupportedSchemaVersion`. Numeric sort guards against future filename mistakes (`2_xxx.sql` would otherwise sort before `10_xxx.sql` lexically); the gap assertion guards against deletion of a numbered migration. Layer 1 unit test `TestMigrationSequenceContiguous` pins both invariants.
- `store.Migrate(ctx, s)` is a standalone function (not a method) that accepts the open store and runs the schema check + any pending migrations inside a single `*sql.Tx`. Callers: the dispatcher for workspace-bound commands (Task 4.2 step 5), and `quest init` explicitly after creating the DB (Task 5.1). Keeping migration out of `Open` is the mechanism that lets `OTEL.md` §8.8's sibling-span contract work — the dispatcher wraps this call in its own `quest.db.migrate` span before creating the command span.
- **Migrations do not use `BeginImmediate`.** `store.Migrate` accesses the underlying `*sql.DB` directly (via an unexported accessor on `*sqliteStore`, e.g. `(s).db()`) and calls `db.BeginTx(ctx, nil)`. Because the DSN carries `_txlock=immediate` (Task 3.1), the returned `*sql.Tx` already holds the IMMEDIATE lock. Avoiding `BeginImmediate` means migrations do not emit a nested `quest.store.tx` span under `quest.db.migrate` and do not need a `migrate` entry in the `TxKind` enum — migrations are orders of magnitude slower than ordinary writes, and including them would distort the `dept.quest.store.tx.duration{tx_kind}` histogram. `OTEL.md` §8.8 already describes `quest.db.migrate` as the only migration span.
- Migration runner: read the stored version via `s.CurrentSchemaVersion(ctx)` (Task 3.3). The method first checks `SELECT name FROM sqlite_master WHERE type='table' AND name='meta'`; if the `meta` table does not exist (fresh DB), it returns `0` (not an error). Compare to `store.SupportedSchemaVersion` (an integer constant in code).
  - `stored > supported` → return `(0, errors.NewSchemaTooNew(stored, supported))`. The helper lives in `internal/errors/errors.go` and produces the spec-pinned wording: `"database schema version N is newer than this binary supports -- upgrade quest (binary supports M)"`, wrapping `ErrGeneral` (exit 1). The dispatcher (Task 4.2 step 5) uses the same helper so the two emission sites cannot drift; spec §Storage pins this exact wording in prose, and a single constructor is the single source of truth.
  - `stored == supported` → return `(0, nil)`.
  - `stored < supported` → apply each pending migration inside the single `*sql.Tx`. Migration 001 is responsible for creating the `meta` table and setting `schema_version = 1`. Return `(count, nil)` where `count` is the number of migration files actually executed. If any step fails, rollback, leave the DB at the prior version, and return `(0, err)`. Do **not** proceed with partial migration.
- The returned `applied` count populates the `quest.schema.applied_count` attribute on the `quest.db.migrate` span (`OTEL.md` §8.8 / §4.3). `store.Migrate` is the only component that knows which SQL files actually ran — callers cannot synthesize this from version math because migration files may be added, renumbered, or skipped across binary versions. The dispatcher passes the count through to `MigrateSpan`'s end closure: `ctx, end := telemetry.MigrateSpan(ctx, from, to); applied, err := store.Migrate(ctx, s); end(applied, err)`.
- Schema v1 tables (derive from the spec; this is the load-bearing inventory):
  - `meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)` — holds `schema_version` only. Do **not** mirror `id_prefix` from `.quest/config.toml` — the file is the single source of truth and the filesystem layout already binds the DB to it.
  - `tasks(id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', context TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT 'task', status TEXT NOT NULL DEFAULT 'open', role TEXT, tier TEXT, acceptance_criteria TEXT, metadata TEXT NOT NULL DEFAULT '{}', parent TEXT, owner_session TEXT, started_at TEXT, completed_at TEXT, handoff TEXT, handoff_session TEXT, handoff_written_at TEXT, debrief TEXT, created_at TEXT NOT NULL)` with `FOREIGN KEY(parent) REFERENCES tasks(id) ON UPDATE CASCADE` and an index on `parent`, `status`, `(status, role)`. `created_at` is an internal column for query/ordering convenience — it is **not** part of the `quest show` JSON contract (spec §Task Entity Schema does not list it). The `store.Task` Go type must tag it `json:"-"`.
  - `history(id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, timestamp TEXT NOT NULL, role TEXT, session TEXT, action TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}', FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE)` with indexes on `(task_id, timestamp)` and `timestamp`.
    - `payload` is a JSON blob for action-specific fields (`reason`, `fields`, `content`, `target`, `link_type`, `old_id`, `new_id`, `url`). Keep it opaque at the schema layer; marshal/unmarshal in Go per action per `quest-spec.md` §History field.
  - `dependencies(task_id TEXT NOT NULL, target_id TEXT NOT NULL, link_type TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (task_id, target_id, link_type), FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE, FOREIGN KEY(target_id) REFERENCES tasks(id) ON UPDATE CASCADE)` — uniqueness on `(task, target, type)` per `quest-spec.md` §Multi-type links. Index on `target_id` for reverse traversal (`retry-of` detection).
  - `tags(task_id TEXT NOT NULL, tag TEXT NOT NULL, PRIMARY KEY (task_id, tag), FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE)` with an index on `tag`.
  - `prs(task_id TEXT NOT NULL, url TEXT NOT NULL, added_at TEXT NOT NULL, PRIMARY KEY (task_id, url), FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE)` — append-only, idempotent per spec §Idempotency.
  - `notes(id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, timestamp TEXT NOT NULL, body TEXT NOT NULL, FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE)` with an index on `(task_id, timestamp)`.
  - `task_counter(prefix TEXT PRIMARY KEY, next_value INTEGER NOT NULL)` for the project-global top-level ID counter; `subtask_counter(parent_id TEXT PRIMARY KEY, next_value INTEGER NOT NULL, FOREIGN KEY(parent_id) REFERENCES tasks(id) ON UPDATE CASCADE ON DELETE NO ACTION)` for per-parent sub-task counters. (See Task 4.1.) The `ON UPDATE CASCADE` ensures `quest move` rewrites counter rows automatically alongside the task-id rename, so sub-task numbering never collides with a moved parent. `ON DELETE NO ACTION` preserves the counter row even if a task is hard-deleted (none are in v0.1, but the invariant stays: counters outlive their parent — a cancelled parent does not reuse sub-task numbers).
- **FK cascade strategy.** Every side table (`history`, `dependencies` both columns, `tags`, `prs`, `notes`) declares `FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE`. `quest move` (Task 8.3) relies on this: updating `tasks.id` causes SQLite to cascade every side-table FK to the new ID in the same transaction. No `PRAGMA defer_foreign_keys` is needed — cascades fire immediately regardless, and every intermediate state is FK-consistent because the root `UPDATE` rewrites all references atomically (see Task 8.3's "No `defer_foreign_keys` pragma is needed" bullet, confirmed by `TestMoveSubgraphFKIntegrity`'s `PRAGMA foreign_key_check` assertion). This collapses Task 8.3's six manual cross-table UPDATEs to one (plus history append) and makes `quest tag` / `quest untag` on a missing task fail with a constraint error that maps cleanly to `ErrNotFound`. Handlers still do explicit existence checks before the FK fires so the error message can cite the missing ID; the FK is defense-in-depth.
- Emit a `slog.InfoContext` when a migration runs, per `OBSERVABILITY.md` §Log Levels.

**Tests:** Layer 3 + Layer 1 migration tests per `TESTING.md` §migration_test.go:

- Fresh DB → migration runs → `schema_version == 1`.
- Pre-seeded fixture DB at `schema_version = 1` → no migrations applied; version unchanged.
- Fixture DB at `schema_version = 2` against a binary that supports only 1 → returns an error, DB untouched. Build the fixture via a documented helper script in `internal/store/testdata/migrations/` per `TESTING.md` §Store Fixtures.

**Done when:** `store.Open` on a fresh path yields a DB at version 1 with every table present; downgrade protection works.

---

### Task 3.3 — Store interface skeleton

**Deliverable:** the `type Store interface { ... }` declaration landed in Phase 2 (per the Phase 2 implementation-order note); Task 3.3 completes the file by adding Go types (`store.Task`, `store.History`, etc.) and the unexported `*sqliteStore` with stub method bodies returning `errors.New("not implemented")` until they land alongside each command in Phase 5+.

**Spec anchors:** `quest-spec.md` §Task Entity Schema, §Status Lifecycle, §Worker Commands, §Planner Commands; `OTEL.md` §8.3 (the decorator wraps the interface).

**Implementation notes:**

- Types: `store.Task`, `store.History`, `store.Dependency`, `store.Note`, `store.PR`, `store.Tx`. JSON tags match the field names in `quest-spec.md` §Core fields / §Execution fields — this is a contract.
- **Handler-owns-SQL model.** The `Store` interface is narrow: read methods plus a transaction primitive plus schema-version lookup. Write handlers run their own SQL inside a `*store.Tx` returned by `BeginImmediate`. The interface does **not** include coarse methods like `SetStatus`/`UpdateFields`/`AppendNote`/`AppendPR`/`SetHandoff` — those would be leaky (each command's UPDATE shape is bespoke). Decorator wrapping happens at the `BeginImmediate` seam.
- **Full `Store` interface signatures** (declare all now, implement later):
  ```go
  type Store interface {
      // Reads
      GetTask(ctx context.Context, id string) (Task, error)
      GetTaskWithDeps(ctx context.Context, id string) (Task, error)       // Task with denormalized Dependencies
      ListTasks(ctx context.Context, filter Filter) ([]Task, error)
      GetHistory(ctx context.Context, id string) ([]History, error)
      GetChildren(ctx context.Context, parentID string) ([]Task, error)
      GetDependencies(ctx context.Context, id string) ([]Dependency, error)  // outgoing edges
      GetDependents(ctx context.Context, id string) ([]Dependency, error)    // incoming edges
      GetTags(ctx context.Context, id string) ([]string, error)
      GetPRs(ctx context.Context, id string) ([]PR, error)
      GetNotes(ctx context.Context, id string) ([]Note, error)
      // Lifecycle
      Close() error
      BeginImmediate(ctx context.Context, kind TxKind) (*Tx, error)
      CurrentSchemaVersion(ctx context.Context) (int, error)
  }
  ```
  `Filter` is a value struct with nullable / zero-value fields per spec §`quest list`: `Statuses []string`, `Parents []string`, `Tags []string` (all entries AND-combined — see below), `Roles []string`, `Types []string`, `Tiers []string`, `Ready bool`, `Columns []string`. Empty slices / false booleans mean "no filter"; the query builder in `ListTasks` AND/ORs per the spec §`quest list` semantics. Note that `Tags` is a flat `[]string`: every tag entered (whether via a single comma-separated value or across repeated `--tag` flags) is ANDed — `--tag go,auth --tag concurrency` requires `go AND auth AND concurrency`, matching the spec and Task 10.2's tag semantics. The flat shape keeps the SQL builder straightforward; audit rendering for span attributes (if ever needed) computes the joined view from the flat slice. **`Filter.Columns` is a projection hint, not a filter** — it does not narrow the row set, it tells the SQL builder which auxiliary JOINs/subqueries to include: include the `tags` JOIN when `Columns` mentions `tags`, the `dependencies` JOIN when `Columns` mentions `blocked-by`, the children-count subquery when `Columns` mentions `children`. Fields not requested are returned as zero values on the `store.Task` struct (empty slices or empty strings); the rendering layer in Task 10.2 projects the requested column set to the output row. The two concerns (which rows, which columns) sit in one struct because the cost of the optional JOINs is the load-bearing decision — separating them would not change the SQL builder's logic. Layer-3 test `TestListTasksColumnsControlsJoins` asserts the `tags` field is empty on returned `Task`s when `Columns` excludes `tags`.
- **ID allocation lives in `internal/ids/`, not on the `Store` interface.** The `ids.NewTopLevel(ctx, tx *store.Tx, prefix string) (string, error)` and `ids.NewSubTask(ctx, tx *store.Tx, parent string) (string, error)` helpers (Task 4.1) own both the counter read-modify-write and the base36/base10 formatting — one logical operation, one package. The counter SQL runs on the caller's live `*store.Tx`, so atomicity with the task insert is preserved without widening the `Store` interface. Do not add `NextTaskID` / `NextSubTaskID` methods to `Store`; they would duplicate the same operation in two packages.
- **`store.AppendHistory` is a package-level function, not a `Store` method.** Signature: `func AppendHistory(ctx context.Context, tx *store.Tx, h History) error`. The `History.Action` field is typed as `store.HistoryAction string` with exported constants for every spec-allowed value: `HistoryCreated`, `HistoryAccepted`, `HistoryCompleted`, `HistoryFailed`, `HistoryCancelled`, `HistoryReset`, `HistoryMoved`, `HistoryNoteAdded`, `HistoryPRAdded`, `HistoryFieldUpdated`, `HistoryLinked`, `HistoryUnlinked`, `HistoryTagged`, `HistoryUntagged`, `HistoryHandoffSet`. Typing prevents handler-side typos at compile time. Contract test `TestHistoryActionEnum` (Task 13.1) iterates the constants and asserts each value matches an entry in spec §History field. Every write handler calls `AppendHistory` inside its transaction after the primary UPDATE/INSERT. `AppendHistory` is the sole call site that converts empty `Role` and `Session` to `sql.NullString{}` — this keeps the nullable-column contract (`quest show` emits `null`, not `""`) enforced at a single point. It is not on the interface because it takes a raw `*store.Tx`, lives inside the store package's implementation, and does not need the decorator's span-wrapping (the `quest.store.tx` span already covers the surrounding transaction).
- **Interface-first shape enables the decorator.** `InstrumentedStore` (Task 12.4) stores `inner Store` and wraps each method one-to-one; the key wrap point is `BeginImmediate`, where the decorator populates the `onCommit` / `onRollback` hook fields on the returned `*store.Tx` so its `Commit`/`Rollback` methods close a `quest.store.tx` span and record `quest.tx.rows_affected`, `quest.tx.outcome`, and `quest.tx.lock_wait_ms`. Test fakes in `internal/testutil/` implement `Store` directly.
- Every status transition (`accept`, `complete`, `fail`, `reset`, `cancel`) runs inside `BeginImmediate(ctx, TxKind<value>)` — leaves and parents alike — per the spec's §Storage > Atomicity update (see Task 6.2 rationale). Write handlers own the UPDATE/INSERT SQL; the transaction boundary is the only OTEL seam.
- Errors from the store must map cleanly to `errors.Err*` sentinels (`ErrNotFound` when the target row doesn't exist, `ErrConflict` when a precondition fails, `ErrTransient` when the busy_timeout is exceeded — detect `sqlite3 error code 5 (SQLITE_BUSY)` in the driver).

**Tests:** none at this task boundary — tests land with each real implementation.

**Done when:** the package compiles, `make test` passes (empty bodies acceptable), and all handler packages can reference the right interface method signatures.

---

## Phase 4 — CLI skeleton

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

---

## Phase 5 — `quest init`

### Task 5.1 — Implement `quest init --prefix PREFIX`

**Deliverable:** `internal/command/init.go` — creates `.quest/`, writes `config.toml`, opens the DB, applies schema v1, exits 0.

**Spec anchors:** `quest-spec.md` §`quest init`, §Prefix validation, §Tool Identity; `STANDARDS.md` §Config File.

**Implementation notes:**

- Because `init` runs _before_ a workspace exists, it takes a different config-discovery path: do not walk up; operate in CWD. If `.quest/` exists in CWD or any ancestor (use `config.DiscoverRoot` before creating), exit 5 with `quest: conflict: .quest/ already exists at <path>`.
- `init` is dispatched with `RequiresWorkspace=false` (Task 4.2), so `config.Validate` is **not** called. `config.Load` still runs and populates whatever it can from flags/env; the handler only reads `cfg.Agent` (for history attribution) and the flag-supplied `--prefix`.
- Validate `--prefix` via `ids.ValidatePrefix`. Any failure → exit 2 naming the rule.
- Write `.quest/config.toml` with:

  ```toml
  # Role gating — AGENT_ROLE values that unlock elevated commands.
  elevated_roles = ["planner"]

  # Task IDs (immutable for this project's lifetime).
  id_prefix = "<validated prefix>"
  ```

  Use `os.WriteFile` with mode `0o644`. The `.quest/` directory is `0o755`.

- Open the DB at `.quest/quest.db` via `store.Open`, wrap it with `telemetry.WrapStore(s)`, and run migrations inside `telemetry.MigrateSpan` so the migration is still observable:
  ```go
  cwd, err := os.Getwd()
  if err != nil { return err }
  dbPath := filepath.Join(cwd, ".quest/quest.db")
  s, err := store.Open(dbPath)
  if err != nil { return err }
  defer s.Close()
  s = telemetry.WrapStore(s)
  from, err := s.CurrentSchemaVersion(ctx)
  if err != nil { return err }
  migCtx, end := telemetry.MigrateSpan(ctx, from, store.SupportedSchemaVersion)
  applied, err := store.Migrate(migCtx, s)
  end(applied, err)
  if err != nil { return err }
  ```
  **Init derives the DB path locally, not from `cfg.Workspace.DBPath`.** `cfg.Workspace.DBPath` is populated from `cfg.Workspace.Root`, which is empty when `config.DiscoverRoot` does not find a `.quest/` — exactly the state `quest init` runs in. Every other workspace-bound command uses `cfg.Workspace.DBPath`; init is the one carve-out because it is the handler that *creates* the workspace. Compute the path post-mkdir via `filepath.Join(cwd, ".quest/quest.db")` and do not rely on the resolved config value. Because init is the one command where migration runs from inside the handler (rather than via the dispatcher's pre-handler step), `quest.db.migrate` ends up as a *child* of the `execute_tool quest.init` command span — the documented carve-out in `OTEL.md` §8.8. Metrics increment identically to the sibling case. A failed migration leaves the DB at the prior version (empty, in the init case) per spec §Storage — no cleanup needed. Re-running `quest init` after a migration failure is safe because `config.toml` is already written and the DB is either empty or intact. **Note:** init is the only handler-path caller of `telemetry.WrapStore`; every other command receives an already-wrapped store from the dispatcher. Do not copy this pattern into other handlers.
- Output JSON matches spec §`quest init`: `{"workspace": "<absolute path to .quest/>", "id_prefix": "<prefix>"}` — both fields always present. The `workspace` field is the absolute path to the `.quest/` directory itself (e.g. `/abs/path/to/project/.quest`), not the workspace root. `TestInitOutputShape` (Task 13.1) uses `filepath.Base(workspace) == ".quest"` (cross-platform, avoids Windows `\` separators) so a regression that emits the root is caught immediately. In `--format text` the output is the bare absolute `.quest/` path followed by a single newline — no prefix, no framing, no prefix echo (spec §`quest init`).

**Tests:** Layer 4 CLI:

- Happy path: fresh tempdir → exit 0, files created, JSON output contains both fields.
- Bad prefix → exit 2 with the specific rule in the stderr message.
- Re-running init → exit 5 `already exists`.
- Missing `--prefix` → exit 2 usage error.

Layer 3: confirm the DB opens at schema version 1 and all tables exist.

**Done when:** `quest init --prefix tst` in a fresh dir produces a workable `.quest/` that subsequent commands (once implemented) can open.

---

## Phase 6 — Worker commands

These are the smallest surface and every downstream command depends on them. Implement them before any planner command.

### Task 6.1 — `quest show [ID] [--history]`

**Deliverable:** `internal/command/show.go` plus the store reads it needs.

**Spec anchors:** `quest-spec.md` §`quest show` (full JSON shape, dependency denormalization, field ordering).

**Implementation notes:**

- If `ID` is omitted, default to `cfg.Agent.Task`. If that's also empty, return `ErrUsage` with `"no task ID provided and AGENT_TASK is unset"`.
- `store.GetTaskWithDeps(ctx, id)` performs one query for the task row, one for dependencies (joined with the target task to pick up `title` and `status` — spec requires these denormalized onto the dependency array), one for tags, one for PRs, and one for notes.
- **Missing ID returns exit 3.** When no task row matches, return `ErrNotFound` — exit 3 per spec §Error precedence. No partial response, no empty object on stdout, no dependency fetch. The contract test (`TestShowJSONHasRequiredFields`) implicitly covers this; being explicit here prevents an accidental empty-body response.
- `--history` adds `history []History`; without it, the returned object has no `history` field at all. Spec §`quest show` documents this as the sole carve-out to "all fields always present."
- **Go marshaling pattern for the `history` carve-out.** Declare the response struct's history field as `History *[]History \`json:"history,omitempty"\``. With a pointer plus `omitempty`: nil pointer → field absent (default-show); non-nil pointer to an empty slice → field present as `[]` (`--history` against a task with no history); populated pointer → field present with rows. The plain `[]History` + `omitempty` shape cannot satisfy both the default-absent and `--history`-with-empty-history-as-`[]` invariants simultaneously (any `omitempty` slice flattens both nil and empty to omitted). Handler sets `History` to `&history` when `--history` is passed, leaves it nil otherwise. `TestShowHistoryFieldPresence` (Task 13.1) covers all three cases: default → absent; `--history` empty → present as `[]`; `--history` populated → present with rows.
- **Telemetry wiring** (Phase 12): after loading the task row, call `telemetry.RecordTaskContext(ctx, id, tier, taskType)` so the command span carries the mandatory task-affecting attributes (`quest.task.id`, `quest.task.tier`, `quest.task.type`) per `OTEL.md` §4.3. Centralizing in a single helper ensures uniform coverage across every task-affecting handler (`show`, `accept`, `update`, `complete`, `fail`, `cancel`, `move`, `deps`, `tag`, `untag`); new handlers simply call the helper.
- **History `payload` flattening.** When serializing a history entry, unmarshal the stored `payload` JSON blob into a `map[string]any` and merge its keys into the top level of the entry object alongside `timestamp`, `role`, `session`, `action`. Spec §History field shows `reason`, `fields`, `content`, `target`, `link_type`, `old_id`, `new_id`, `url` at the top level of the emitted entry — the flat shape is a contract, but the opaque blob is the storage shape. This flattening also applies to `quest export` (Task 11.1's `history.jsonl`).
- JSON field order in the emitted object matches the spec example exactly. Go's `encoding/json` preserves struct field order — define a struct per command output rather than `map[string]any`.

**Tests:** Layer 2 contract test: `TestShowJSONHasRequiredFields` per `STANDARDS.md` §CLI Output Contract Tests. Layer 3 handler test for happy path, missing task (exit 3), `--history` flag changes the payload shape.

**Done when:** `quest show` round-trips every spec-listed field (including `null` / `[]` / `{}`), and the contract test is green.

---

### Task 6.2 — `quest accept [ID]`

**Deliverable:** `internal/command/accept.go`.

**Spec anchors:** `quest-spec.md` §`quest accept` (leaf vs parent path, race handling), §Parent Tasks §Enforcement rules, §Status Lifecycle, §Idempotency.

**Implementation notes:**

- Route every path through `tx, err := s.BeginImmediate(ctx, store.TxAccept)` — leaves and parents alike. The `tx_kind` enum (`OTEL.md` §4.3 / §5.3) describes the operation category, not the structural shape, so a single value covers both cases. Do **not** use the atomic-UPDATE-with-RowsAffected shortcut for leaves: it conflates exit 3 (`not_found`, task ID unknown) and exit 5 (`conflict`, task exists in wrong status). The spec (§Storage > Atomicity and §Error precedence) requires every status transition to use `BEGIN IMMEDIATE` with SELECT-then-UPDATE regardless of shape.
- Inside the transaction:
  1. `tx.QueryRow("SELECT status FROM tasks WHERE id=?", id).Scan(&status)`. `sql.ErrNoRows` → `ErrNotFound` (exit 3). This is safe from TOCTOU because `BEGIN IMMEDIATE` holds the write lock from the start.
  2. If status is not `open` → `ErrConflict` (exit 5).
  3. If the task has children (`SELECT 1 FROM tasks WHERE parent=? LIMIT 1`), verify every child is terminal (`complete`/`failed`/`cancelled`). Any non-terminal → collect IDs + statuses and return `ErrConflict` with a structured body.
  4. `tx.Exec("UPDATE tasks SET status='accepted', owner_session=?, started_at=? WHERE id=?", ownerSess, now, id)` — where `ownerSess = sql.NullString{String: cfg.Agent.Session, Valid: cfg.Agent.Session != ""}` so an unset `AGENT_SESSION` persists as SQL `NULL` (spec §Task Entity Schema: "`null` when unset"), not `""`. `quest show` then emits JSON `null` without a read-side coercion layer.
  5. `store.AppendHistory(ctx, tx, History{Action: "accepted", Role: cfg.Agent.Role, Session: cfg.Agent.Session})` — the package-level helper (Task 3.3) converts empty Role/Session to `sql.NullString{}` at the single write site.
  6. `tx.Commit()`.
- The extra row read is rounding error under SQLite's serialized-writer model; unifying the code path makes all four exit codes (3/4/5/6) reachable and eliminates a test-vs-spec mismatch.
- `started_at` uses the cross-cutting rule from the plan preamble: `time.Now().UTC().Format(time.RFC3339)`. Second precision, UTC, Z-terminated.
- **Stdout on success** is the action-ack shape per spec §Write-command output shapes: `{"id": "<id>", "status": "accepted"}`. Both fields always present; `status` is the literal string `"accepted"` on success.
- Emit structured conflict output per `OBSERVABILITY.md` §Output Contract: when exit 5 is due to non-terminal children, stdout gets the conflict object too. Shape:
  ```json
  {
    "error": "conflict",
    "task": "proj-a1",
    "non_terminal_children": [{ "id": "proj-a1.1", "status": "accepted" }]
  }
  ```
- **Accept on a non-open task (any of `accepted`, `complete`, `failed`, `cancelled`) emits exit 5 with an empty stdout and the standard stderr two-liner.** The vigil-coordination cancelled-body (`{"error":"conflict","task":"...","status":"cancelled","message":"task was cancelled"}`) from spec §In-flight worker coordination is scoped to `update`/`complete`/`fail` — it tells vigil to route a mid-flight worker's debrief. Accept is not in that coordination set (a worker that hasn't accepted yet has no in-flight debrief to route), so it emits no structured body on stdout. All four non-open from-statuses are handled uniformly: stderr carries `quest: conflict: task is not in open status (current: <status>)` + `quest: exit 5 (conflict)`, stdout is empty. The structured `non_terminal_children` body (exit 5 due to non-terminal children on parent accept) is a separate case and *is* emitted on stdout per the shape above. Do not reflexively copy the cancelled-body emitter from Task 6.4 into this handler.
- **Telemetry wiring** (Phase 12): after the SELECT in step 1, call `telemetry.RecordTaskContext(ctx, id, tier, taskType)` so the command span carries the §4.3 task-affecting attributes (per H3 — every task-affecting handler must call this; load `tier` / `type` in the same SELECT). On success call `telemetry.RecordStatusTransition(ctx, id, "open", "accepted")` (no-op until Task 12.5, but the call site must exist).

**Tests:** Layer 2 idempotency (re-accepting a non-open task → exit 5). Layer 3: leaf happy path, leaf already-accepted (exit 5), leaf not-found (exit 3), parent with non-terminal child (exit 5 + structured body), parent with all terminal (success). Layer 5 concurrency lives in Task 13.2 (`TestConcurrentAcceptLeavesOnlyOneWinner` — N goroutines race on a single `open` task; exactly one wins, the other N-1 receive exit 5 with no silent loss). Match `TESTING.md` §Concurrency Tests on the goroutine count (10).

**Done when:** the `TestConcurrentAcceptLeavesOnlyOneWinner` sketch in `TESTING.md` §Concurrency Tests compiles and passes.

---

### Task 6.3 — `quest update [ID] [flags]`

**Deliverable:** `internal/command/update.go`.

**Spec anchors:** `quest-spec.md` §`quest update` (worker vs elevated flags, terminal-state gating), §Input Conventions (`@file` resolution), §Idempotency.

**Implementation notes:**

- Expand `@file` arguments via an `*input.Resolver` — **each handler constructs its own** at entry: `r := input.NewResolver(stdin); value, err := r.Resolve("--debrief", raw)`. One handler per invocation means the "one resolver per invocation" invariant already holds without adding a parameter to every handler's signature. `internal/input/` (package from Task 0.1) exports `type Resolver struct { ... }` with `NewResolver(stdin io.Reader) *Resolver` and `(r *Resolver) Resolve(flag, val string) (string, error)`. `@-` reads stdin, `@path` reads the file relative to CWD, bare strings pass through. Size cap, missing-file behavior, and error format come from spec §Input Conventions > Size limit: cap at 1 MiB (1,048,576 bytes). Use **source-aware phrasing** in errors: oversized stdin → `ErrUsage` with `"<flag>: stdin exceeds 1 MiB limit (observed <N> bytes)"`; oversized file → `"<flag>: file @<path> exceeds 1 MiB limit (observed <N> bytes)"`; missing/unreadable file → `"<flag>: failed to read @<path>: <os error>"`. The flag name is always the leading token so agents can programmatically route on it. Both map to exit 2. The `Resolver` instance tracks whether `@-` has been consumed and rejects a second `@-` on the same invocation with `"stdin already consumed by <first-flag>; at most one @- per invocation"` (exit 2). State lives on the instance, not the package — parallel tests and nested invocations stay isolated.
- Worker flags: `--note`, `--pr`, `--handoff`. Elevated flags: `--title`, `--description`, `--context`, `--type`, `--tier`, `--role`, `--acceptance-criteria`, `--meta KEY=VALUE` (repeatable).
  - `--meta` parsing: split on `=` once. Reject `--meta foo` (no `=`) and `--meta =bar` (empty key) with `ErrUsage` (exit 2) and a message naming the offending flag. Empty *value* (`--meta foo=`) is also `ErrUsage` — spec §`quest update` requires a value per key.
  - **Empty-value rejection** (spec §`quest update`): `--role ""`, `--handoff ""`, `--title ""`, `--description ""`, `--context ""`, `--acceptance-criteria ""`, and `--note ""` all return `ErrUsage` (exit 2) with a message naming the flag. There is no clear-field mechanism in v0.1; passing an empty string is always a planner-side mistake.
  - **`--type` transition check** (spec §`quest update`): when `--type task` is requested, `SELECT 1 FROM dependencies WHERE task_id=? AND link_type IN ('caused-by','discovered-from') LIMIT 1` inside the transaction. Any match → `ErrConflict` (exit 5) with a body listing the blocking links. The predicate checks **outgoing** links only — `dependencies.task_id` is the source, `dependencies.target_id` is the target, and the spec's invariant applies to the source's type. A retrospective project with accumulated `discovered-from`/`caused-by` edges must still allow retyping of upstream targets (the incoming edges do not block). The check must run inside the same transaction as the UPDATE so a concurrent `quest link` cannot slip a link in between the check and the retype.
- Terminal-state gating per spec §`quest update` *Terminal-state gating*: on `complete` / `failed` tasks, only `--note`, `--pr`, `--meta` are accepted; everything else → `ErrConflict` with a message listing the blocked flags. On `cancelled` tasks, **every** `update` variant (including `--note` / `--pr` / `--meta` / `--handoff`) → `ErrConflict` with the structured body from §*In-flight worker coordination* (`{"error":"conflict","task":"...","status":"cancelled","message":"task was cancelled"}`). Cancelled is the signal that tells vigil to terminate the worker; letting any update through would defeat it.
- **Ownership check scope.** Non-owning worker on an `accepted` task: `ErrPermission` (exit 4). Owning workers OR any elevated role pass. **On `open` tasks the ownership check is skipped** — any worker session can call `quest update --note` / `--handoff` / `--meta` / `--pr` on an `open` task, matching spec §`quest accept`'s phrasing "after acceptance, only the owning session can call `quest update` ... on the task" (implying pre-acceptance the check does not apply). This is the M9 decision; see the cross-cutting "Deliberate deviations from spec" section for rationale and revisit conditions.
- **Every update runs inside `s.BeginImmediate(ctx, store.TxUpdate)`** — no append-only shortcut path. Every `update` invocation has at least the existence + terminal-state preconditions, so the "pure append-only, no precondition" carve-out doesn't match the actual check set. Spec §Storage was updated to require `BEGIN IMMEDIATE` on every `update` regardless of flags — the single-path rule keeps `dept.quest.store.tx{tx_kind=update}` observability uniform across worker-only and mixed-flag invocations, which is load-bearing for the spec's daemon-upgrade signal (sustained exit-7 rate). The cost — one `BEGIN IMMEDIATE` per update on an otherwise-uncontended DB — is microseconds and shows up as real contention only in the regime where the daemon-upgrade signal should already be firing.
- `--meta` uses **read-merge-write semantics** per spec §Idempotency (`update --meta`: "overwrites the value for an existing key; sets it for a new key"). Inside the same transaction: `SELECT metadata FROM tasks WHERE id=?`, `json.Unmarshal` into `map[string]any`, overlay each `KEY=VALUE` from this invocation (later keys in the same invocation overwrite earlier ones; pre-existing keys not on this command line are preserved), re-marshal via `json.Marshal` to canonical JSON, and `UPDATE tasks SET metadata=? WHERE id=?`. Each `KEY=VALUE` pair is a string-valued entry (already JSON-safe); if future work adds `--meta-json` (accepting raw JSON), that path parses the input with `json.Unmarshal` first and emits `ErrUsage` (exit 2) on parse failure, keeping `tasks.metadata` invariant: always valid JSON. Per-key `field_updated` history entries track the delta: for each changed key, emit `{fields: {"metadata.<key>": {from, to}}}` — `from` is `null` when the key was absent before.
- **Mixed-flag gate span.** If any elevated flag is present in the args, the handler emits `telemetry.GateSpan(ctx, cfg.Agent.Role, allowed)` before the elevated-flag check, where `allowed = config.IsElevated(...)`. This is the one case where a handler (not the dispatcher) emits the gate span — `quest update` is dispatched at worker level (so workers can `--note`) but the mixed-flag path still needs the gate observability. Without the handler-side emission, retrospective queries for "how often did workers attempt elevated ops?" would undercount `update --tier` / `--role` attempts.
- Precondition order inside the transaction must match spec §Output & Error Conventions *Error precedence*: existence (3) → role gate on elevated flags (6) → ownership (4) → terminal-state / cancelled gating (5) → `--type` transition check (5) → flag-shape usage errors (2). State checks (5) always fire before usage checks (2) per the spec's deterministic-contract rule — `quest update proj-a1 --type task --tier T99` on a task with a `caused-by` link must produce exit 5 (blocked by link), not exit 2 (bad tier). Do not reorder these checks — agent retry logic switches on the resulting exit code.
- `--handoff` is an upsert — write `handoff`, `handoff_session` (from `AGENT_SESSION`), `handoff_written_at` atomically; append a `handoff_set` history entry with `content` per spec §History field. Survives `quest reset`.
- `--note` appends a `notes` row AND a `note_added` history entry; do NOT include the note body in the history payload (the body lives on the `notes` table).
- `--pr` is idempotent on the URL. If duplicate, skip both the `prs` insert and the history entry per spec §History field. (A clean `INSERT OR IGNORE` works, but you still need to check whether it inserted — `RowsAffected` > 0 → append history.)
- Elevated field edits write a `field_updated` history entry per spec with `{fields: {<name>: {from, to}}}`. Collect old values inside the same transaction before the update.
- **Stdout on success** is the action-ack shape per spec §Write-command output shapes: `{"id": "<id>"}`. No echo of which fields changed — callers run `quest show` for the post-state. Text mode emits a single line (e.g., `proj-a1.3 updated`) and is not a contract.
- **Telemetry wiring** (Phase 12): after loading the task row, call `telemetry.RecordTaskContext(ctx, id, tier, taskType)` so the command span carries the §4.3 task-affecting attributes (per H3 — every task-affecting handler must call this). `update` does not change `status` so `RecordStatusTransition` is not called; the handler emits content recorders only. For each mutated elevated field call the appropriate `telemetry.RecordContentX` (`title`, `description`, `context`, `acceptance_criteria`) when `CaptureContentEnabled()` returns true (Task 12.7's gate-at-call-site pattern). For `--note` call `telemetry.RecordContentNote`; for `--handoff` call `telemetry.RecordContentHandoff`.

**Tests:** Layer 2 (contract idempotency table for `--pr`, `--handoff`), Layer 3 (each flag's happy + failure path, terminal-state gate, ownership check), Layer 4 (the `@file` resolver end-to-end).

**Done when:** every row of the spec's idempotency table for `update` is a passing test case.

---

### Task 6.4 — `quest complete` and `quest fail`

**Deliverable:** `internal/command/complete.go`, `internal/command/fail.go`.

**Spec anchors:** `quest-spec.md` §`quest complete`, §`quest fail`, §Parent Tasks.

**Implementation notes:**

- Both require `--debrief`. `@file` / `@-` resolution runs at arg-parse time via the handler-constructed `*input.Resolver` (Task 6.3); resolution errors (missing file, oversized file, second `@-`) exit 2 immediately, before any DB I/O — those are unrecoverable I/O failures, not shape checks.
- `complete` runs inside `s.BeginImmediate(ctx, store.TxComplete)`; `fail` runs inside `s.BeginImmediate(ctx, store.TxFail)`. The `tx_kind` enum (`OTEL.md` §4.3 / §5.3) keeps dashboards able to distinguish the two outcomes. Same rationale as Task 6.2 for wrapping leaves: unified code path, distinguishable exit codes.
- **Precondition ordering overview.** Arg-parse time fires only `@file` / `@-` resolution errors (exit 2). Every other check — existence, ownership, from-status, children-terminal, and the empty-debrief check — runs inside the transaction in the order below, so that `quest complete nonexistent-task --debrief ""` exits 3 (not found), not 2 (empty debrief). State checks always precede usage checks (spec §Error precedence).
- **Precondition order inside the transaction** (same deterministic-contract rule as Task 6.3, mirroring spec §Error precedence):
  1. **Existence (3).** `SELECT` the task; zero rows → `ErrNotFound` (exit 3).
  2. **Ownership (4, applies to leaves AND parents).** `store.CheckOwnership(tx, taskID, session, elevated) error` — a shared package-level helper used by `complete`, `fail`, and `update`. Signature: the check passes when either `owner_session == session` OR `elevated == true`; otherwise returns `ErrPermission` (exit 4). This applies uniformly to leaf tasks and parent tasks (both the dispatched-verifier path where `owner_session` is set by `accept`, and the lead direct-close path where `owner_session` is null and `elevated` must be true). Spec §`quest accept`: "only the owning session (or an elevated role) can call `quest update`, `quest complete`, or `quest fail` on the task" — unconditional, not leaf-scoped. The prior "leaves only" scoping had a hole: a non-owning worker could close another verifier's accepted parent because the ownership check was skipped on the parent path. The single-line helper now closes it.
  3. **From-status (5).**
     - `complete` accepts `accepted` (dispatched verifier or worker) and `open` (lead direct-close of a parent). Any other → `ErrConflict` (exit 5).
     - `fail` accepts only `accepted`. `open → failed` is not a supported transition.
  3a. **Leaf-direct-close rejection (5, complete only, when from-status is `open`).** Spec §Status Lifecycle scopes the `open → complete` transition to **parent tasks only**. If the from-status is `open`, immediately verify the task has at least one child row (`SELECT 1 FROM tasks WHERE parent=? LIMIT 1`). Zero rows → `ErrConflict` (exit 5) with stderr message `"leaf task cannot be completed from open — accept first"` and `quest.precondition=leaf_direct_close` in the H5 span event. This carve-out closes a hole where an elevated planner running `quest complete LEAF-ID --debrief "…"` against an `open` leaf would otherwise pass through (ownership: elevated; from-status: open allowed; children-terminal: trivially satisfied with zero children) and skip the `accepted` stage entirely. Spec §Parent Tasks §Closing a parent: "Direct-close (lead)…" applies only when the task has children. Without this check, `started_at` stays `null` on the completed task and the `dept.quest.status_transitions{from=open, to=complete}` invariant is violated for leaves.
  4. **Children-terminal (5, parents only — runs for `open → complete` after step 3a confirms parent-ness, and for `accepted → complete|failed` on parents).** If the task has children, verify every child is terminal (`complete`/`failed`/`cancelled`); collect non-terminal IDs + statuses on failure and emit the structured body.
  5. **Empty-debrief usage (2).** A resolved `--debrief` value that is literally the empty string (`""`) → `ErrUsage` (exit 2). Whitespace-only values (e.g., `" "`, `"\t"`) pass this check and are stored as-is — per the M10 decision, the rejection is scoped to the literal empty string only. Last in the ladder so state-level errors (exit 3/4/5) take precedence — see the precondition-ordering overview above. This aligns with spec §`quest update` "Empty values are usage errors" which rejects empty strings uniformly across required free-form flags; whitespace-only is out of scope there too.
- **Cancelled-task rejection.** When the blocking from-status is `cancelled`, emit the structured conflict body on stdout in addition to the stderr exit-5 line, mirroring `quest update` per spec §`quest update` ("`quest complete` and `quest fail` on a cancelled task are rejected for the same reason"):
  ```json
  {"error":"conflict","task":"<id>","status":"cancelled","message":"task was cancelled"}
  ```
  Reuse the emitter introduced in Task 6.3 so the body shape is identical across commands.
- Record `completed_at` (second-precision RFC3339 UTC per the plan's cross-cutting rule) and append history (`action=completed` or `action=failed`).
- `--pr` is accepted on both; append+idempotent semantics as in `update --pr`. When `--pr` introduces a URL not already attached to the task, append a `pr_added` history entry alongside the `completed` / `failed` lifecycle entry (both inside the same transaction, in the order: lifecycle entry first, then `pr_added`). Duplicate URLs (already attached) produce only the lifecycle entry and no `pr_added` entry, consistent with `update --pr`'s idempotence. Spec §History field has been broadened to cover all three commands.
- Debrief text goes into `tasks.debrief`; it is **not** appended to history (history carries the action, not the content). Export writes debriefs as separate markdown files in Task 11.1.
- **Stdout on success** per spec §Write-command output shapes: `complete` emits `{"id": "<id>", "status": "complete"}`; `fail` emits `{"id": "<id>", "status": "failed"}`. Both fields always present.
- **Telemetry wiring** (Phase 12): after loading the task row, call `telemetry.RecordTaskContext(ctx, id, tier, taskType)` so the command span carries the §4.3 task-affecting attributes. On success call `telemetry.RecordStatusTransition(ctx, id, from, to)` with the resolved `from` (`"accepted"` for verifier/worker path, `"open"` for lead direct-close) and `to` (`"complete"` or `"failed"`). Also call `telemetry.RecordTerminalState(ctx, id, tier, role, outcome)` where `outcome` is the literal `"complete"` or `"failed"` — this increments `dept.quest.tasks.completed{tier, role, outcome}` per `OTEL.md` §5.1 / §8.6 with the full dimension set. The recorder is the unified terminal-state emitter shared with `cancel` (Task 8.1) — see Task 2.3 stub list. Call `telemetry.RecordContentDebrief(ctx, debrief)` gated on `CaptureContentEnabled()`. **Do not call** a `RecordPRAdded` recorder — PR additions are tracked via the `pr_added` history row; OTEL.md §5.1 has no `dept.quest.prs` instrument and §4.5 has no `quest.content.pr_url` event because PR URLs are high-cardinality user-supplied strings.

**Tests:** Layer 3: happy paths, parent with non-terminal children (exit 5 + structured body), terminal → terminal attempt (exit 5), missing debrief (exit 2), `TestCompleteFromNonOwningSessionReturnsExit4` and `TestFailFromNonOwningSessionReturnsExit4` (a second session attempts to close a task owned by the first; exits 4 without mutating state), `TestCompleteOnOpenLeafReturnsExit5` (an elevated planner running `quest complete LEAF-ID --debrief "..."` on an `open` leaf is rejected with exit 5 and the `leaf_direct_close` precondition string per C3), and the precondition-order cases (`quest complete nonexistent-task --debrief ""` → exit 3; `quest complete unowned-task --debrief ""` → exit 4; `quest complete own-task --debrief ""` → exit 2). Layer 2 contract test `TestCompleteLeafFromOpenRejected` pins the same C3 carve-out at the contract layer.

**Done when:** lifecycle table `open → accepted → complete|failed` and parent direct-close `open → complete` both work.

---

## Phase 7 — Planner task creation

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

---

## Phase 8 — Task management

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

---

## Phase 9 — Links and tags

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

---

## Phase 10 — Queries

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

---

## Phase 11 — Export

### Task 11.1 — `quest export [--dir PATH]`

**Deliverable:** `internal/export/` package + `internal/command/export.go`.

**Spec anchors:** `quest-spec.md` §`quest export` (layout, idempotency).

**Implementation notes:**

- Default output: `filepath.Join(cfg.Workspace.Root, "quest-export")` — always a sibling of `.quest/`, never relative to CWD. A planner running `quest export` from `<workspace>/src/` writes the archive to `<workspace>/quest-export/`, not `<workspace>/src/quest-export/`. This matches spec §`quest export` ("a sibling of `.quest/`"). Layer 4 CLI test runs `quest export` from a subdirectory and asserts the output path is workspace-root-relative.
- Layout exactly as specified: `tasks/{id}.json` for every task, `debriefs/{id}.md` only for tasks that have a non-empty debrief, `history.jsonl` chronologically across all tasks. **Always create the `debriefs/` directory** even when no task has a debrief — this keeps the export layout stable for consumers that pattern-match the on-disk shape. An empty `debriefs/` directory is valid.
- Task JSON uses the same shape as `quest show --history` (i.e., includes the full history array). Contract test asserts this equivalence.
- Idempotent: re-running overwrites. **Track-and-delete-stale pattern**: (1) write every current `tasks/<id>.json` / `debriefs/<id>.md` (using temp-suffix + `os.Rename` for atomicity per file), (2) rewrite `history.jsonl`, (3) collect the set of written task IDs during the write pass, then (4) delete any `tasks/*.json` / `debriefs/*.md` not in the written set. Deletion runs **after** all writes succeed, so a mid-export failure never clobbers the previous archive. Spec §`quest export` says "overwrites the output directory" — interpret as "makes the output directory reflect current state," which means old files for deleted tasks should be removed. Do **not** remove the output directory first (opens a window where the archive is partial) and do **not** ship the temp-suffix-only pattern without delete (stale files from prior runs would accumulate).
- `history.jsonl` entries: one JSON object per history row, ordered by timestamp ascending across all tasks. Apply the same `payload`-flattening rule as `quest show --history` (Task 6.1): merge the stored `payload` JSON into the top level of each entry so `reason`, `fields`, `content`, `target`, `link_type`, `old_id`, `new_id`, `url` appear flat alongside `timestamp`, `role`, `session`, `action`, `task_id`.

**Tests:** Layer 2 contract: layout matches; task JSON field-for-field matches `quest show --history`. Layer 3: idempotency (run twice, diff the tree — should be byte-identical).

**Done when:** export round-trips the full database and produces files that are human-readable and diff-friendly.

---

## Phase 12 — Telemetry (OTEL)

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

---

## Phase 13 — Contract, concurrency, and CI tests

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

---

## Phase 14 — Ship v0.1

### Task 14.1 — Documentation pass

**Deliverable:** A user-facing `README.md` (separate from the spec) + verify every spec-listed command and flag is dispatchable; confirm `--help` output exists for each command and lists its flags. The exact `--help` text is not a contract — the spec does not define it — so do not assert text equality against spec prose.

### Task 14.2 — Changelog

**Deliverable:** Move `Unreleased` entries to `[v0.1.0] - <date>` in `CHANGELOG.md`. Tag the repo.

---

## Cross-cutting concerns (apply to every phase)

### History recording

Every *state-changing* mutation writes exactly one history row; never batch. Idempotent no-ops (duplicate link add, missing link/tag remove, duplicate PR add) write no state change and emit no history, per spec §Idempotency ("returns exit 0 with no state change"). The `action` enum and action-specific payload shape are defined in `quest-spec.md` §History field — implement once in `store.AppendHistory(ctx, tx, History) error` and call it from every write path; write-path code decides whether `RowsAffected > 0` before calling it.

`AppendHistory` converts empty `role` and `session` strings to `sql.NullString{}` at write time so they persist as SQL `NULL` rather than `""` — spec §History field: "Recorded as `null` if unset." The JSON output path uses `*string` on the Go struct so `encoding/json` emits `null` natively without a helper. Writing correctly at the source keeps direct-SQL inspection accurate and the contract-test `TestHistoryEntryShape` (Task 13.1) green without round-trip coercion.

### Nullable TEXT columns

Every nullable TEXT column on `tasks` that corresponds to a JSON `null`-when-unset field is written with `sql.NullString{String: s, Valid: s != ""}` when the source Go string is empty. This covers `owner_session`, `handoff`, `handoff_session`, `handoff_written_at`, `role`, `tier`, `acceptance_criteria`, `parent`, `debrief`, and every `history.role` / `history.session`. The rule lives with `AppendHistory` for history and with each handler's UPDATE for task-row writes (see Task 6.2 for the accept example). Do not retrofit this at the read side — direct SQLite inspection must see `NULL`, not `''`.

### Timestamps

All timestamps are written as `time.Now().UTC().Format(time.RFC3339)` — second precision, UTC, Z-terminated. Applies to `started_at`, `completed_at`, `handoff_written_at`, every `history.timestamp`, every `notes.timestamp`, and the PR `added_at`. Spec §Output & Error Conventions. Sub-second precision is intentionally not used: the single-writer model makes collisions at second precision unlikely, and uniform second precision keeps downstream parsing simple.

### JSON field presence

Every struct that marshals to command output uses explicit `json:"..."` tags and emits `null` / `[]` / `{}` for empty values — never omit. Add a contract test for every command output; the set is non-negotiable per `STANDARDS.md` §CLI Surface Versioning.

### Error messages

User-facing stderr lines: `quest: <class>: <actionable message>` followed by `quest: exit N (<class>)`. The slog record carries the wrapped error. Never leak SQL, file paths from internal sources, or type names to stderr. See `OBSERVABILITY.md` §Sanitization.

### `@file` input

Any flag listed in `quest-spec.md` §Input Conventions goes through an `*input.Resolver` that **each handler constructs at entry** (`r := input.NewResolver(stdin)`). Handlers call `r.Resolve("--debrief", raw)` to expand `@file` / `@-` / bare-string inputs. Adding new flags that accept free-form text? Add them to the spec list and call `r.Resolve` for them in the handler. The "one handler per invocation" property means the "one resolver per invocation" invariant already holds without adding the resolver to the handler signature — revisit if handlers ever share per-invocation state (a second resolver, a rate limiter, etc.).

The `*input.Resolver` keeps per-invocation state: once `@-` has been resolved for one flag, a second `@-` on the same invocation returns `ErrUsage` (exit 2) with `"stdin already consumed by <first-flag>; at most one @- per invocation"` — this rule is spec-owned (`quest-spec.md` §Input Conventions) because agent retry logic depends on the contract. Stdin is a single byte stream; consuming it twice yields empty content or a block on the second read, and silent corruption is worse than an explicit rejection. Tests exercising the second-`@-` rejection, oversized-file, missing-file, and binary-content paths live in `internal/input/resolve_test.go` (one central suite, not distributed across handler tests).

### Telemetry call sites

`cli.Execute` owns `CommandSpan` / `WrapCommand` per `OTEL.md` §8.2 — command handlers do not call either. Handlers receive a context that already carries the command span and call `telemetry.RecordX` at every observable event (status transition, link add/remove, batch outcome, query result count, precondition failure, cycle detection). Per the M5 decision there is no general-purpose `SpanEvent` helper; every span event ships through a named recorder in the §8.6 inventory. Handlers never import `go.opentelemetry.io/otel/trace` or `go.opentelemetry.io/otel/attribute` — the Task 2.3 grep tripwire enforces this. The no-op stubs make these calls safe during Phase 2–11; Phase 12 lights them up. Do not gate calls on a telemetry-enabled check — the `enabled()` helper is package-private to `internal/telemetry/` (`OTEL.md` §8.3), and the no-op SDK providers already make the hot path cheap.

### Precondition-failed events (`OTEL.md` §13.3)

Every exit-5 path in every handler must emit `quest.precondition.failed` via `telemetry.RecordPreconditionFailed(ctx, precondition string, blockedByIDs []string)`. The `precondition` argument is a bounded enum: `children_terminal`, `parent_not_open`, `ownership`, `from_status`, `existence`, `type_transition`, `cycle`, `depth_exceeded`, `cancelled`, `move_history_accepted`, `move_parent_accepted`, `leaf_direct_close` (introduced by C3). Handlers populate `blockedByIDs` only when the precondition is structurally about other tasks (`children_terminal` lists the non-terminal child IDs; `cycle` lists the cycle path; otherwise nil/empty). The recorder applies the §13.3 truncation limits (≤ 10 IDs, ≤ 256 chars total) via the shared `truncateIDList` helper. Affected handlers (must include a `RecordPreconditionFailed` call on every exit-5 path):

- Task 6.2 (`accept`) — `from_status` (non-open accept), `children_terminal` (parent with non-terminal children).
- Task 6.3 (`update`) — `from_status` (terminal-state gating), `cancelled` (cancelled-task rejection), `type_transition` (`--type task` blocked by outgoing `caused-by`/`discovered-from` link), `ownership`.
- Task 6.4 (`complete`/`fail`) — `from_status`, `children_terminal`, `cancelled`, `ownership`, `leaf_direct_close` (C3).
- Task 7.1 (`create`) — `parent_not_open`, `depth_exceeded`, plus per-edge `cycle` / `blocked_by_cancelled` / etc. via `deps.ValidateSemantic`.
- Task 8.1 (`cancel`) — `from_status` (terminal-state cancel rejection).
- Task 8.3 (`move`) — `move_history_accepted`, `move_parent_accepted`, `parent_not_open`, `depth_exceeded`, cycle (circular parentage).
- Task 9.1 (`link`) — `cycle`, semantic constraint violations.

Without these events, dashboards lose the per-precondition breakdown and the §13.3 "trace-first vs log-first debugging" duality breaks. The `TestPreconditionFailedEventShape` contract test (Task 13.1) iterates the exit-5 inventory and asserts the event fires with the matching enum value on every handler.

### Slog event emission (`OTEL.md` §3.2)

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

### Schema evolution

Any change to the DB shape is a numbered migration. Bump `schema_version`. Add a `migration_test.go` fixture at the new version. Never edit an existing migration — the binary's supported-version set is forward-only.

### Duration calculation

Durations recorded on spans and metrics use `float64(elapsed.Microseconds()) / 1000.0`, never `elapsed.Milliseconds()`. The latter truncates sub-millisecond durations to 0, which destroys p50 / p95 signal on fast commands (every quest command that doesn't hit disk finishes in single-digit milliseconds). `OTEL.md` §19 checklist pins this; the plan mirrors the rule here so every duration-emitting call site follows it.

### Integration build tags

Any test file exercising the store, goroutines, or the built binary uses `//go:build integration` per `TESTING.md` §Integration Build Tags. Tasks 5.1, 6.x, 7.x, 8.x, 9.x, 10.x, 11.1 describe Layer 3 / 4 / 5 tests without always restating the build-tag requirement — treat this cross-cutting rule as authoritative.

### Deliberate deviations from spec

Some plan decisions tighten or extend the spec. Track them here so a future reader inspecting divergence can find the rationale in one place. Revisit each entry when its "revisit if" condition is triggered:

- **Unknown `--columns` / `--status` / `--type` / `--tier` values on `quest list` are rejected with exit 2** (Task 10.2). Spec is silent on unknown-value handling for these filter flags; rejecting at parse time with a `cli.Suggest`-powered "did you mean" hint prevents silent partial output (unknown column) and silent empty-result typos (unknown status/type/tier — e.g., `--status compelete` returning `[]` instead of an error). Revisit if agents need forward-compatible filter specs.
- **`--help` is gated by workspace and role checks** (Task 4.2). Spec is silent; plan retains gating rather than following common CLI convention (git / kubectl / docker all short-circuit `--help`). Rationale: quest is agent-first, and a worker asking for help on a command it cannot execute indicates a calling-code bug — exit 6 (role denied) or exit 2 (no workspace) gives the agent an actionable signal that matches what the command itself would return, instead of usage text the agent will never act on. `quest help <cmd>` and `quest --help` remain gate-free for human discovery (role-filtered banner). Revisit if human-operator workflows surface real friction.
- **`quest export` deletes stale output files** (Task 11.1). Spec §`quest export` says "re-running overwrites the output directory." Plan extends this to remove files for tasks that no longer exist (moved via `quest move`, cancelled and recreated, etc.) so the archive is a true snapshot. Revisit if a concrete workflow relies on stale files surviving.
- **`invalid_link_type` batch error code** (Task 7.3 / spec §Batch error output). Added as a new code at phase `semantic` so typos in `link_type` produce a clear diagnostic instead of falling through to `source_type_required`. Spec has been amended; this entry exists for audit trail.
- **Empty `--reason` on cancel / reset records `null`** (Tasks 8.1 / 8.2). Spec is silent; plan treats empty value as equivalent to omitting the flag, asymmetric with `quest update` where empty strings are exit-2 errors. Rationale: `--reason` annotates a state transition rather than attaching task data. Spec has been amended with this behavior.
- **Non-owning worker `update` on open tasks is permitted** (Task 6.3). Spec §`quest accept` says "after acceptance, only the owning session (or an elevated role) can call `quest update` ... on the task" — implying pre-acceptance the ownership check does not apply. Plan allows any worker session to call `quest update --note` / `--handoff` / `--meta` / `--pr` on an `open` task (ownership check runs only on `accepted` tasks). Rationale: the accept-before-update flow is the designed path, but a worker with `AGENT_TASK` set on a not-yet-accepted task is a plausible pre-accept state (e.g., vigil has assigned the task but the worker wants to record a startup note before calling `accept`). Tightening pre-acceptance would require a spec change and introduce a worker-surface policy decision beyond the M9 scope. Revisit if retrospective queries surface confusing attribution (notes from a session that never accepted).
- **Whitespace-only `--debrief` is accepted, literal empty string is rejected** (Task 6.4). Spec §`quest complete` / `quest fail` say "debrief is required" and spec §`quest update` pins "Empty values are usage errors" (exit 2). Plan rejects literal `""` (matching the spec rule for sibling free-form flags) but passes through whitespace-only values as legal debrief content. Per the M10 decision, this is a deliberate narrowing: "required" means non-empty-byte-string, not non-whitespace content. Revisit if retrospectives show planners/workers submitting whitespace-only debriefs to satisfy the required-flag check.

### Agent discipline

If the spec is silent on a question you need answered, stop and resolve it in the spec first. Do not guess. Do not delete or rename existing error classes, exit codes, or JSON fields without a deprecation cycle and a `CHANGELOG.md` entry.

---

## Dependency graph of phases

```
Phase 0 ──► Phase 1 ──► Phase 2 ──► Phase 3 ──► Phase 4 ──► Phase 5 ──► Phase 6
                                       │                                   │
                                       │                                   ▼
                                       │                                Phase 7
                                       │                                   │
                                       │                  ┌────────┬──────┴───────┬────────┐
                                       │                  ▼        ▼              ▼        ▼
                                       │              Phase 8  Phase 9       Phase 10  Phase 11
                                       │                  │        │              │        │
                                       ▼                  └────────┴──────┬───────┴────────┘
                                   Phase 12                               ▼
                                                                      Phase 13
                                                                          │
                                                                          ▼
                                                                      Phase 14
```

Edges:

- Phase 0–6 are strictly sequential: each builds infrastructure the next depends on.
- **Phase 7** (create + dep validator + batch) depends on Phase 6 (worker commands) only via the store + ID generator — it does not need `accept`/`complete`/`fail` handlers at runtime. In practice, Phase 6 lands first because it proves out the store.
- **Phase 8** (cancel, reset, move) depends on Phase 7 (it modifies tasks + subgraphs created by `create` / `batch`) but **not** on Phase 9/10/11.
- **Phase 9** (link, tag) depends on Phase 7 only. Independent of Phase 8.
- **Phase 10** (queries) depends on Phase 7 + the ID generator. Independent of Phase 8 and Phase 9 — `list --tag` just returns empty when no tags exist yet.
- **Phase 11** (export) depends on Phase 3 (schema stable) and Phase 6 (full task read path). Independent of Phases 7–10 structurally, but in practice runs after them so there is meaningful data to export.
- **Phase 12** (OTEL) depends only on Task 2.3 (no-op telemetry stub). It can run in parallel with Phases 6–11 because the no-op stubs keep handler signatures stable.
- **Phase 13** (contract + concurrency tests) runs after Phase 11 to catch regressions from both the command surface and the telemetry pass.

**Where two agents can fork:** after Phase 7 lands, Phases 8 / 9 / 10 / 11 can run in any order or in parallel. Phase 12 can run in parallel with any of Phases 6–11.

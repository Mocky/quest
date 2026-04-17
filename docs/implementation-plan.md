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

The plan review surfaced open spec questions. Resolved questions (cancelled-update behavior, `update` precondition precedence, batch `parent` ref/id shape, `quest version` JSON shape, stderr trace-ID enrichment, module path, `CommandSpan` return shape) are now specified in the relevant anchor doc and the corresponding plan task. No entries are outstanding.

Flag any additional ambiguities discovered during implementation here before coding around them.

---

## Phase 0 — Repository bootstrap

### Task 0.1 — Initialize the Go module and directory skeleton

**Deliverable:** `go.mod`, `go.sum`, `cmd/quest/main.go`, an empty `internal/` tree matching the package map below, `Makefile`, `CHANGELOG.md`, `.gitignore`.

**Spec anchors:** `AGENTS.md` §Folder structure, `STANDARDS.md` §Changelog, `OTEL.md` §10.1.

**Implementation notes:**

- Module path: `github.com/Mocky/quest`. Record this in `go.mod` and in every test import; do not use a placeholder.
- Go version: `1.23` or later. The `iter.Seq` signature used in Task 4.3 requires 1.23; also picks up `slog` / `log/slog` stability.
- Create these empty-but-committed package directories with one `doc.go` per package describing the package's role:
  ```
  cmd/quest/                  main, global flag parsing, dispatch, SDK lifecycle
  internal/cli/               command dispatcher, global flag parsing, role gate shim
  internal/command/           one file per command handler (accept.go, create.go, ...)
  internal/config/            the single config package (see Phase 1)
  internal/logging/           slog setup, fan-out handler shell
  internal/errors/            exit-code + error-class mapping
  internal/ids/               prefix validation, short-ID generation
  internal/store/             SQLite layer, migrations, tasks/history/deps/tags
  internal/batch/             quest batch four-phase validator
  internal/output/            json/text rendering helpers
  internal/export/            quest export layout generator
  internal/telemetry/         OTEL package (added as a no-op shell in Task 2.3, filled in Task 12.1)
  internal/testutil/          shared test helpers (workspace, store, CLI runner)
  internal/buildinfo/         version string wired with -ldflags
  ```
- `cmd/quest/main.go` must be a two-function shell per `OTEL.md` §7.1:
  ```go
  func main() { os.Exit(run()) }
  func run() int { ... }
  ```
  No handler ever calls `os.Exit`.
- `Makefile` targets: `build`, `test` (`go test ./...`), `test-all` (`go test -race -tags integration -count=1 ./...`), `cover`, `lint`, `ci`.
- `CHANGELOG.md` seeded with a `## [Unreleased]` block and the sections `Added / Changed / Deprecated / Removed / Fixed / Schema` per `STANDARDS.md` §Changelog.
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
- Version command runs the standard startup flow (config load, logging init, `telemetry.Setup`). It does **not** need a special early-exit path — the no-op OTEL providers from `OTEL.md` §7.2 make telemetry setup zero-cost when disabled, and version works with no workspace because it never opens the DB.
- Per `OTEL.md` §4.2, `version` is explicitly suppressed from span and metric emission — no `CommandSpan` call, no operation counter increment. Suppression happens at dispatch time inside `cli.Execute`, not by skipping setup.

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

**Deliverable:** `config.Config` struct, `config.Flags` struct, `config.Load(flags Flags) (Config, error)` that produces a fully resolved config or returns all validation errors at once.

**Spec anchors:** `STANDARDS.md` Part 1 (full section, especially §Config Struct, §Validation, §Defaults), `quest-spec.md` §Role Gating (env var names), `OBSERVABILITY.md` §Correlation Identifiers.

**Implementation notes:**

- Shape exactly as laid out in `STANDARDS.md` §Config Struct: `Workspace`, `Agent`, `Log`, `Telemetry`.
- Precedence: `flag > env var > .quest/config.toml > default`. Implement with a `firstNonEmpty` helper.
- `Agent` fields come from `AGENT_ROLE`, `AGENT_TASK`, `AGENT_SESSION`. Empty string is a valid state — do not substitute anything.
- `Log.Level` default `"warn"`, `Log.OTELLevel` default `"info"`. These are the only two logging knobs per `OBSERVABILITY.md` §Logger Setup.
- `Telemetry.CaptureContent` is parsed with `strconv.ParseBool` on `OTEL_GENAI_CAPTURE_CONTENT`.
- `Validate()` collects every error into a single returned string joined by newlines. Message format: `<source>: <what's wrong>` per `STANDARDS.md` §Validation.
- This package is also the only reader of `.quest/config.toml`. `ReadFile` from Task 1.1 is called from `Load`.
- Export one helper: `config.IsElevated(role string, elevated []string) bool` — used by the role gate in Phase 4. Keep the gate decision centralized.

**Tests:** Layer 1 tests in `config_test.go`:

- Env var resolution using `t.Setenv` (never `os.Setenv`).
- Flag override beats env var beats file default.
- `Validate()` collects multiple errors; message contains all offending sources.
- `IsElevated`: empty role, role present in list, role absent, list empty.

**Done when:** tests pass, and a grep for `os.Getenv` in the codebase finds only `internal/config/` (and later `internal/telemetry/env.go` per `OTEL.md` §8.2).

---

## Phase 2 — Logging and errors

### Task 2.1 — `internal/logging/`: slog stderr handler + level/format parsing

**Deliverable:** `logging.NewStderrHandler(cfg StderrConfig) slog.Handler`, `logging.LevelFromString(s string) slog.Level`, a fan-out handler type that wraps N children, and a `logging.Setup(cfg config.LogConfig) *slog.Logger` entry point.

**Spec anchors:** `OBSERVABILITY.md` §Logger Setup, §Log Levels, §Standard Field Names; `OTEL.md` §3.1 (the OTEL bridge plugs into this fan-out in Task 12.1).

**Implementation notes:**

- Use `slog.NewTextHandler` under the hood for stderr. Quest does not need JSON logs on stderr — human-readable is the contract.
- The fan-out handler accepts `[]slog.Handler` and dispatches every `Handle`/`Enabled`/`WithAttrs`/`WithGroup` call to every child. It is level-gated at each child, not centrally.
- In Phase 2 the fan-out has one child (stderr). Task 12.1 adds the OTEL bridge as the second child without modifying callers.
- Register the resulting logger via `slog.SetDefault(...)` in `cmd/quest/run()` so that non-context call sites (config loading, SDK init) work without extra plumbing.
- **Trace-ID enrichment on stderr.** Per `OBSERVABILITY.md` §Correlation Identifiers, stderr slog records must carry `trace_id` and `span_id` when a span is active on the record's context. Implement this with a thin `traceEnrichHandler` that wraps the stderr text handler: in `Handle(ctx, r)`, call a `telemetry.TraceIDsFromContext(ctx) (traceID, spanID string, ok bool)` helper and, if `ok`, `r.AddAttrs(slog.String("trace_id", traceID), slog.String("span_id", spanID))` before delegating to the child.
- Keep the OTEL API import inside `internal/telemetry/` per `OTEL.md` §10.1. The Phase 2 no-op `telemetry.TraceIDsFromContext` returns `("", "", false)`, so `internal/logging/` never imports `go.opentelemetry.io/otel/trace` directly. Task 12.1 replaces the helper with a real implementation backed by `trace.SpanContextFromContext`; the stderr handler code does not change.
- In the no-op / disabled path, the helper returns `ok=false` and stderr records simply omit the trace fields, matching `OBSERVABILITY.md` §Correlation Identifiers "when available".

**Tests:** Layer 1 tests:

- Level parsing: `"debug"`, `"DEBUG"`, `"info"`, `"warn"`, `"error"`, `""` (→ default), `"garbage"` (→ error).
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

**Deliverable:** `telemetry.Setup(ctx, cfg) (shutdown func(context.Context) error, err error)`, `telemetry.CommandSpan(ctx, cmd string, elevated bool) (context.Context, trace.Span)`, `telemetry.WrapCommand(ctx, cmd string, elevated bool, fn func(context.Context) error) error`, a family of `RecordX` stubs, `telemetry.TraceIDsFromContext(ctx) (traceID, spanID string, ok bool)` (used by the stderr handler in Task 2.1), and a `telemetry.Enabled() bool` helper — all no-ops for now.

**Spec anchors:** `OTEL.md` §8.1 (package layout), §8.2 (CommandSpan shape and `WrapCommand` wrapper), §8.6 (recorder functions), §10.1 (API-only import boundary).

**Implementation notes:**

- In Phase 2, `telemetry.Setup` returns a no-op shutdown and installs nothing. `CommandSpan` returns the input context and a `trace.Span` that is `trace.SpanFromContext(ctx)` (the non-recording background span — valid to `End()`, valid to `SetStatus`, cheap). `WrapCommand` simply calls `fn(ctx)` and returns its error. `RecordX` functions are empty. This lets command handlers call the real signatures from day one, so Task 12.1 is a drop-in replacement.
- The stub _may_ import `go.opentelemetry.io/otel/trace` for the `trace.Span` return type (API-only, not SDK) — that matches `OTEL.md` §10.1's "API yes, SDK only in setup.go" boundary. All other OTEL imports stay out until Task 12.1.
- Expose exactly the symbols listed in `OTEL.md` §8.2 and §8.6 so callers don't change signatures when the real implementation arrives. `CommandSpan` returns a raw `trace.Span`; `WrapCommand` wraps the three-step error recording from §4.4 for handlers that don't need mid-handler span control.

**Tests:** Layer 1 — call each stub, assert no panic, no allocation in the disabled path (benchmarks land in Task 12.5).

**Done when:** `grep -r "go.opentelemetry.io" internal/ cmd/` finds nothing; every downstream package that will eventually emit telemetry already calls `telemetry.RecordX` stubs.

---

## Phase 3 — Storage foundation

### Task 3.1 — `internal/store/`: SQLite open, WAL, busy_timeout

**Deliverable:** `store.Open(path string) (*Store, error)` and `store.Close() error`, plus `store.TxKind` enum (`accept_parent`, `create_child`, `complete_parent`, `move`, `cancel_recursive`).

**Spec anchors:** `quest-spec.md` §Storage (WAL mode, `PRAGMA busy_timeout = 5000`, serialized writes).

**Implementation notes:**

- Driver: `modernc.org/sqlite` (pure Go, no CGo — preferred for CLI portability). Import as `_ "modernc.org/sqlite"`; DSN: `file:<path>?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on`.
- Double-set the pragmas after open (`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=on;`) so they are applied to the primary connection regardless of DSN handling.
- `*Store` wraps `*sql.DB`. Do not cap `MaxOpenConns` — the driver already serializes writes at the SQLite layer, and readers are free under WAL.
- Expose `store.BeginImmediate(ctx) (*sql.Tx, error)` — wraps `db.BeginTx(ctx, &sql.TxOptions{})` followed by `exec("BEGIN IMMEDIATE")`. Rationale: Go's `database/sql` does not have a first-class `BEGIN IMMEDIATE` hook, and quest needs the early write-lock acquisition that the spec mandates.

**Tests:** Layer 3 integration tests (tagged `//go:build integration`):

- Open creates the file and applies the pragmas (`SELECT ... FROM pragma_journal_mode`).
- Concurrent readers do not block a writer (open two `*Store` instances against the same DB file, run a long read and a write concurrently).

**Done when:** `store.Open` returns a live handle and `PRAGMA journal_mode` returns `wal`.

---

### Task 3.2 — Migration framework + schema v1

**Deliverable:** `internal/store/migrate.go` with a numbered-migration runner, `internal/store/migrations/001_initial.sql` containing the full initial schema, and `store.SchemaVersion` / `store.SupportedSchemaVersion` constants.

**Spec anchors:** `quest-spec.md` §Storage (`schema_version` meta table, forward-only migrations, newer-than-supported refuses to run), `STANDARDS.md` §Schema Migration Rules.

**Implementation notes:**

- Migrations are embedded via `//go:embed migrations/*.sql`.
- Migration runner: read `meta.schema_version` (create the `meta` table if it's missing — that's part of migration 001 itself); compare to `store.SupportedSchemaVersion` (an integer constant in code).
  - `stored > supported` → return `ErrGeneral` wrapping a clear message: `"database schema version N is newer than this binary supports -- upgrade quest"`. Exit 1 per spec.
  - `stored == supported` → no-op, return nil.
  - `stored < supported` → apply each pending migration inside a single `*sql.Tx`. If any step fails, rollback and leave the DB at the prior version. Do **not** proceed with partial migration.
- Schema v1 tables (derive from the spec; this is the load-bearing inventory):
  - `meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)` — holds `schema_version` only. Do **not** mirror `id_prefix` from `.quest/config.toml` — the file is the single source of truth and the filesystem layout already binds the DB to it.
  - `tasks(id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', context TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT 'task', status TEXT NOT NULL DEFAULT 'open', role TEXT, tier TEXT, acceptance_criteria TEXT, metadata TEXT NOT NULL DEFAULT '{}', parent TEXT, owner_session TEXT, started_at TEXT, completed_at TEXT, handoff TEXT, handoff_session TEXT, handoff_written_at TEXT, debrief TEXT, created_at TEXT NOT NULL)` with `FOREIGN KEY(parent) REFERENCES tasks(id)` and an index on `parent`, `status`, `(status, role)`. `created_at` is an internal column for query/ordering convenience — it is **not** part of the `quest show` JSON contract (spec §Task Entity Schema does not list it). The `store.Task` Go type must tag it `json:"-"`.
  - `history(id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, timestamp TEXT NOT NULL, role TEXT, session TEXT, action TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}')` with indexes on `(task_id, timestamp)` and `timestamp`.
    - `payload` is a JSON blob for action-specific fields (`reason`, `fields`, `content`, `target`, `link_type`, `old_id`, `new_id`, `url`). Keep it opaque at the schema layer; marshal/unmarshal in Go per action per `quest-spec.md` §History field.
  - `dependencies(task_id TEXT NOT NULL, target_id TEXT NOT NULL, link_type TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (task_id, target_id, link_type))` — uniqueness on `(task, target, type)` per `quest-spec.md` §Multi-type links. Index on `target_id` for reverse traversal (`retry-of` detection).
  - `tags(task_id TEXT NOT NULL, tag TEXT NOT NULL, PRIMARY KEY (task_id, tag))` with an index on `tag`.
  - `prs(task_id TEXT NOT NULL, url TEXT NOT NULL, added_at TEXT NOT NULL, PRIMARY KEY (task_id, url))` — append-only, idempotent per spec §Idempotency.
  - `notes(id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, timestamp TEXT NOT NULL, body TEXT NOT NULL)` with an index on `(task_id, timestamp)`.
  - `task_counter(prefix TEXT PRIMARY KEY, next_value INTEGER NOT NULL)` for the project-global top-level ID counter; `subtask_counter(parent_id TEXT PRIMARY KEY, next_value INTEGER NOT NULL)` for per-parent sub-task counters. (See Task 4.1.)
- Emit a `slog.InfoContext` when a migration runs, per `OBSERVABILITY.md` §Log Levels.

**Tests:** Layer 3 + Layer 1 migration tests per `TESTING.md` §migration_test.go:

- Fresh DB → migration runs → `schema_version == 1`.
- Pre-seeded fixture DB at `schema_version = 1` → no migrations applied; version unchanged.
- Fixture DB at `schema_version = 2` against a binary that supports only 1 → returns an error, DB untouched. Build the fixture via a documented helper script in `internal/store/testdata/migrations/` per `TESTING.md` §Store Fixtures.

**Done when:** `store.Open` on a fresh path yields a DB at version 1 with every table present; downgrade protection works.

---

### Task 3.3 — Store interface skeleton

**Deliverable:** `store.Store` methods covering read and write operations the command handlers will call. Signatures only, with `not implemented` bodies. The real implementations land alongside each command in Phase 5+.

**Spec anchors:** `quest-spec.md` §Task Entity Schema, §Status Lifecycle, §Worker Commands, §Planner Commands.

**Implementation notes:**

- Types: `store.Task`, `store.History`, `store.Dependency`, `store.Note`, `store.PR`. JSON tags match the field names in `quest-spec.md` §Core fields / §Execution fields — this is a contract.
- Methods to declare now (implementation comes later):
  - Reads: `GetTask(ctx, id)`, `GetTaskWithDeps(ctx, id)`, `ListTasks(ctx, filter)`, `GetHistory(ctx, id)`, `GetChildren(ctx, parentID)`, `GetDependencies(ctx, id)`, `GetDependents(ctx, id)`, `GetTags(ctx, id)`, `GetPRs(ctx, id)`, `GetNotes(ctx, id)`.
  - Writes: `CreateTask(ctx, t Task)`, `UpdateFields(ctx, id string, updates map[string]any, hist History)`, `SetStatus(ctx, id, from, to, hist History, preCheck func(tx) error)`, `AppendNote`, `AppendPR`, `SetHandoff`, `AddLink`, `RemoveLink`, `AddTags`, `RemoveTags`, `CancelRecursive`, `Move`.
  - Identity: `NextTaskID(ctx, prefix string) (string, error)`, `NextSubTaskID(ctx, parentID string) (string, error)`.
- Every write method that requires a structural transaction accepts a `TxKind` and routes through `BeginImmediate`. Simple single-row writes (append-only, idempotent upserts, leaf status transitions) use `UPDATE ... WHERE` with `RowsAffected` checks per `quest-spec.md` §Atomicity.
- Errors from the store must map cleanly to `errors.Err*` sentinels (`ErrNotFound` when the target row doesn't exist, `ErrConflict` when a precondition fails, `ErrTransient` when the busy_timeout is exceeded — detect `sqlite3 error code 5 (SQLITE_BUSY)` in the driver).

**Tests:** none at this task boundary — tests land with each real implementation.

**Done when:** the package compiles, `make test` passes (empty bodies acceptable), and all handler packages can reference the right method signatures.

---

## Phase 4 — CLI skeleton

### Task 4.1 — ID generator (`internal/ids/generator.go`)

**Deliverable:** `ids.NewTopLevel(ctx, tx *sql.Tx, prefix string) (string, error)` and `ids.NewSubTask(ctx, tx *sql.Tx, parent string) (string, error)`. These are called _inside_ a transaction so concurrent allocators can't collide.

**Spec anchors:** `quest-spec.md` §Task IDs (format, base36 for top-level, base10 for sub-tasks, 2-char min width, 3-level depth cap, structural immutability).

**Implementation notes:**

- Top-level: increment `task_counter[prefix].next_value`; format as base36, left-pad to min width 2. Concretely: values 1–35 render as `01`–`0z`; 36–1295 render as `10`–`zz`; 1296 is the first 3-char ID and renders as `100`; 46656 is the first 4-char ID and renders as `1000`.
- Sub-task: increment `subtask_counter[parent_id].next_value`; format as base10 without padding. ID = `parent + "." + N`.
- Expose `ids.Depth(id string) int` and `ids.ValidateDepth(id string) error` — depth is the count of `.` segments + 1. Reject depth > 3.
- `ids.Parent(id string) string` returns the parent ID (`"proj-01.1.2"` → `"proj-01.1"`, `"proj-01"` → `""`). This is what the move / graph commands use.
- Counters live in the same transaction as the task insert so that a rolled-back insert does not permanently consume an ID.
- Short-ID width is monotonically non-decreasing: the minimum is 2, but once the counter crosses 1296 (`zz`) the formatter naturally produces 3-char IDs. Do not retroactively rewrite older 2-char IDs.

**Tests:** Layer 1:

- `ValidateDepth("proj-01") == nil`, depth 2, depth 3 OK; depth 4 returns error.
- `Parent` for every level.
- Base36 formatting: round-trip the first 1500 values; assert width 2 for 1..1295, width 3 for 1296..46655.

Layer 3 (with store): concurrent `NextTaskID` calls return distinct IDs — 50 goroutines, each opening its own transaction via `BeginImmediate`, all calling `NextTaskID(prefix)`; assert 50 distinct IDs returned with no duplicates.

**Done when:** unit + integration tests pass; ID collisions are structurally impossible under normal use.

---

### Task 4.2 — Global flag parsing + command dispatcher (`internal/cli/`)

**Deliverable:** `cli.Execute(ctx, args []string, stdin io.Reader, stdout, stderr io.Writer) int` — the single entry point called from `main.run()`.

**Spec anchors:** `STANDARDS.md` §Flag Overrides (global flags are position-independent), `quest-spec.md` §Output & Error Conventions, `OBSERVABILITY.md` §Output Contract.

**Implementation notes:**

- Parse global flags (`--format`, `--log-level`, `--color`) in a first pass that ignores unknown flags — they belong to the subcommand. Use a small hand-rolled parser; `flag` alone does not support position-independent globals cleanly.
- After globals are extracted, the first remaining positional is the command name. Everything else goes to a per-command parser.
- Dispatch table: `map[string]commandDescriptor` where each descriptor carries `Handler` (the command function) and `Elevated bool` (whether the command requires an elevated role). Handler signature: `func(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) error`.
- On an unknown command: return `ErrUsage` with a message listing valid commands.
- On missing workspace (discovery error): `quest init` and `quest version` proceed; everything else returns `ErrUsage` with a `"not in a quest workspace — run 'quest init --prefix PFX' first"` message. Exit 2.
- **Dispatch sequence** (this ordering is load-bearing for OTEL span parenthood per `OTEL.md` §4.1 and §8.7):
  1. Parse global flags and identify the command.
  2. Look up the descriptor; determine `elevated` from the table (no I/O).
  3. Call `telemetry.CommandSpan(ctx, name, elevated)` — creates the root `execute_tool quest.<name>` span with `quest.role.elevated` attribute pre-populated from the table lookup, not from the gate result.
  4. If `elevated`, call `telemetry.RoleGate(ctx, cfg.Agent.Role, cfg.Workspace.ElevatedRoles)` — creates the `quest.role.gate` child span, evaluates `config.IsElevated`, and records `quest.role.allowed`. On deny, return `ErrRoleDenied` (exit 6) immediately; the command span records the error via the standard pattern. Log `role gate denied` at INFO per `OBSERVABILITY.md` §Boundary Logging.
  5. Invoke the handler with the context carrying the command span.
  6. `version` is suppressed at step 3 per `OTEL.md` §4.2 — no command span, no metric increment.
- Centralizing the gate in dispatch (rather than in each elevated handler) keeps the security boundary in one place and matches OTEL.md's parent-child span structure.
- Workers see a minimal usage banner when they run `quest` or `quest --help` — list only worker commands. Planners see the full list. This is a context-window concern, not a cosmetic one.

**Tests:** Layer 4 CLI tests:

- Global flags accepted before and after the command name.
- Unknown command → exit 2 with `quest: usage_error: ...`.
- Worker role invoking `quest create` → exit 6 with `quest: role_denied: ...`.
- No workspace → exit 2 with the required message; `quest version` still works.

**Done when:** a structural end-to-end path (no real handlers yet) returns the correct exit codes for the happy and error cases above.

---

### Task 4.3 — Output renderer (`internal/output/`)

**Deliverable:** `output.Emit(w io.Writer, format string, value any) error`, `output.EmitJSONL(w io.Writer, values iter.Seq[any]) error`, helpers for table rendering (`output.Table`) and tree rendering (`output.Tree`).

**Spec anchors:** `quest-spec.md` §Output & Error Conventions (flat JSON, `null`/`[]`/`{}` never omitted), `STANDARDS.md` §CLI Surface Versioning (JSON is a contract).

**Implementation notes:**

- `json` mode: `json.NewEncoder(w).SetIndent("", "")` — compact, one final newline. The pretty-printed examples in `quest-spec.md` are for readability only; agents parse compact output.
- `text` mode column behavior: fixed default widths when `w` is not a TTY; auto-size to terminal width when it is. Use `golang.org/x/term` (or `x/sys/unix` directly) to query width; fall back to 80 columns when detection fails.
- Table truncation uses a trailing `...` per `quest-spec.md` §Text-mode formatting. Never split multi-byte runes — walk back to a rune boundary.
- Provide `output.NullString(s *string) any` so task fields that are `*string` in Go serialize as `null` rather than `""` when unset. This is the field-presence contract: _every_ spec-listed field appears in every response.
- Provide `output.AssertSchema(t *testing.T, got []byte, required []string)` in `internal/testutil/` for contract tests (used heavily in Phase 6+).

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
- Validate `--prefix` via `ids.ValidatePrefix`. Any failure → exit 2 naming the rule.
- Write `.quest/config.toml` with:

  ```toml
  # Role gating — AGENT_ROLE values that unlock elevated commands.
  elevated_roles = ["planner"]

  # Task IDs (immutable for this project's lifetime).
  id_prefix = "<validated prefix>"
  ```

  Use `os.WriteFile` with mode `0o644`. The `.quest/` directory is `0o755`.

- Open the DB at `.quest/quest.db` and run migrations. A failed migration leaves the DB at the prior version (empty, in the init case) per spec §Storage — no cleanup needed. Re-running `quest init` after a migration failure is safe because `config.toml` is already written and the DB is either empty or intact.
- Output JSON: `{"workspace": "<absolute path>", "id_prefix": "<prefix>"}`. Text mode: `initialized quest workspace at <path> with prefix <prefix>`.

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
- `--history` adds `history []History`; without it, the returned object has no `history` field at all. (This is an exception to "all fields always present" and is spelled out in the spec: history is costed out by default.)
- JSON field order in the emitted object matches the spec example exactly. Go's `encoding/json` preserves struct field order — define a struct per command output rather than `map[string]any`.

**Tests:** Layer 2 contract test: `TestShowJSONHasRequiredFields` per `STANDARDS.md` §CLI Output Contract Tests. Layer 3 handler test for happy path, missing task (exit 3), `--history` flag changes the payload shape.

**Done when:** `quest show` round-trips every spec-listed field (including `null` / `[]` / `{}`), and the contract test is green.

---

### Task 6.2 — `quest accept [ID]`

**Deliverable:** `internal/command/accept.go`.

**Spec anchors:** `quest-spec.md` §`quest accept` (leaf vs parent path, race handling), §Parent Tasks §Enforcement rules, §Status Lifecycle, §Idempotency.

**Implementation notes:**

- Route every path through `BeginImmediate` with `TxKind=accept_parent` (a single kind covers both leaf and parent cases — the check differs, the transaction shape does not). Do **not** use the atomic-UPDATE-with-RowsAffected shortcut for leaves: it conflates exit 3 (`not_found`, task ID unknown) and exit 5 (`conflict`, task exists in wrong status), and the spec requires the two to be distinguishable.
- Inside the transaction:
  1. `SELECT status FROM tasks WHERE id=?`. Zero rows → `ErrNotFound` (exit 3). This is safe from TOCTOU because `BEGIN IMMEDIATE` holds the write lock from the start.
  2. If status is not `open` → `ErrConflict` (exit 5).
  3. If the task has children (`SELECT 1 FROM tasks WHERE parent=? LIMIT 1`), verify every child is terminal (`complete`/`failed`/`cancelled`). Any non-terminal → collect IDs + statuses and return `ErrConflict` with a structured body.
  4. `UPDATE tasks SET status='accepted', owner_session=?, started_at=? WHERE id=?`.
  5. Append a history row: `action=accepted`, `role=cfg.Agent.Role`, `session=cfg.Agent.Session`.
- The extra row read is rounding error under SQLite's serialized-writer model; unifying the code path makes all four exit codes (3/4/5/6) reachable and eliminates a test-vs-spec mismatch.
- Populate `owner_session` from `cfg.Agent.Session` (empty string if unset), `started_at` from `time.Now().UTC().Format(time.RFC3339)`.
- Emit structured conflict output per `OBSERVABILITY.md` §Output Contract: when exit 5 is due to non-terminal children, stdout gets the conflict object too. Shape:
  ```json
  {
    "error": "conflict",
    "task": "proj-a1",
    "non_terminal_children": [{ "id": "proj-a1.1", "status": "accepted" }]
  }
  ```
- Call `telemetry.RecordStatusTransition(ctx, id, "open", "accepted")` (no-op until Task 12.5, but the call site must exist).

**Tests:** Layer 2 idempotency (re-accepting a non-open task → exit 5). Layer 3: leaf happy path, leaf already-accepted (exit 5), leaf not-found (exit 3), parent with non-terminal child (exit 5 + structured body), parent with all terminal (success). Layer 5 concurrency (later): two goroutines race, exactly one wins.

**Done when:** the `TestConcurrentAcceptLeavesOnlyOneWinner` sketch in `TESTING.md` §Concurrency Tests compiles and passes.

---

### Task 6.3 — `quest update [ID] [flags]`

**Deliverable:** `internal/command/update.go`.

**Spec anchors:** `quest-spec.md` §`quest update` (worker vs elevated flags, terminal-state gating), §Input Conventions (`@file` resolution), §Idempotency.

**Implementation notes:**

- Expand `@file` arguments in one helper: `input.Resolve(val string, stdin io.Reader) (string, error)`. `@-` reads stdin, `@path` reads the file relative to CWD. Cap at 1 MB to be safe; reject larger with a usage error.
- Worker flags: `--note`, `--pr`, `--handoff`. Elevated flags: `--title`, `--description`, `--context`, `--type`, `--tier`, `--role`, `--acceptance-criteria`, `--meta KEY=VALUE` (repeatable). `--meta` parsing: split on `=` once; reject empty keys.
- Terminal-state gating per spec §`quest update` *Terminal-state gating*: on `complete` / `failed` tasks, only `--note`, `--pr`, `--meta` are accepted; everything else → `ErrConflict` with a message listing the blocked flags. On `cancelled` tasks, **every** `update` variant (including `--note` / `--pr` / `--meta` / `--handoff`) → `ErrConflict` with the structured body from §*In-flight worker coordination* (`{"error":"conflict","task":"...","status":"cancelled","message":"task was cancelled"}`). Cancelled is the signal that tells vigil to terminate the worker; letting any update through would defeat it.
- Non-owning worker on an accepted task: `ErrPermission` (exit 4). Owning workers OR any elevated role pass.
- Precondition order inside the transaction must match spec §Output & Error Conventions *Error precedence*: existence (3) → role gate on elevated flags (6) → ownership (4) → terminal-state / cancelled gating (5) → flag-shape usage errors (2). Do not reorder these checks -- agent retry logic switches on the resulting exit code, and the reviewer flagged this as a deterministic-exit-code requirement.
- `--handoff` is an upsert — write `handoff`, `handoff_session` (from `AGENT_SESSION`), `handoff_written_at` atomically; append a `handoff_set` history entry with `content` per spec §History field. Survives `quest reset`.
- `--note` appends a `notes` row AND a `note_added` history entry; do NOT include the note body in the history payload (the body lives on the `notes` table).
- `--pr` is idempotent on the URL. If duplicate, skip both the `prs` insert and the history entry per spec §History field. (A clean `INSERT OR IGNORE` works, but you still need to check whether it inserted — `RowsAffected` > 0 → append history.)
- Elevated field edits write a `field_updated` history entry per spec with `{fields: {<name>: {from, to}}}`. Collect old values inside the same transaction before the update.

**Tests:** Layer 2 (contract idempotency table for `--pr`, `--handoff`), Layer 3 (each flag's happy + failure path, terminal-state gate, ownership check), Layer 4 (the `@file` resolver end-to-end).

**Done when:** every row of the spec's idempotency table for `update` is a passing test case.

---

### Task 6.4 — `quest complete` and `quest fail`

**Deliverable:** `internal/command/complete.go`, `internal/command/fail.go`.

**Spec anchors:** `quest-spec.md` §`quest complete`, §`quest fail`, §Parent Tasks.

**Implementation notes:**

- Both require `--debrief`; empty or missing → `ErrUsage`.
- Both commands run inside `BeginImmediate(TxKind=complete_parent)` for every task (leaf and parent alike) — same rationale as Task 6.2: unified code path, distinguishable exit codes.
- Inside the transaction, `SELECT` the task; zero rows → `ErrNotFound` (exit 3). Then check the valid from-statuses:
  - `complete` accepts `accepted` (dispatched verifier or worker) and `open` (lead direct-close of a parent). Any other → `ErrConflict`.
  - `fail` accepts only `accepted`. `open → failed` is not a supported transition; a lead cancels an undispatched task, not fails it.
- If the task has children, verify every child is terminal (`complete`/`failed`/`cancelled`); collect non-terminal IDs + statuses on failure.
- Record `completed_at` and append history (`action=completed` or `action=failed`).
- `--pr` is accepted on both; append+idempotent semantics as in `update --pr`.
- Debrief text goes into `tasks.debrief`; it is **not** appended to history (history carries the action, not the content). Export writes debriefs as separate markdown files in Task 11.1.

**Tests:** Layer 3: happy paths, parent with non-terminal children (exit 5 + structured body), terminal → terminal attempt (exit 5), missing debrief (exit 2).

**Done when:** lifecycle table `open → accepted → complete|failed` and parent direct-close `open → complete` both work.

---

## Phase 7 — Planner task creation

### Task 7.1 — `quest create`

**Deliverable:** `internal/command/create.go`.

**Spec anchors:** `quest-spec.md` §Task Creation, §Parent Tasks §Enforcement rules, §Graph Limits, §Dependency validation.

**Implementation notes:**

- Flags per the spec table. `--title` is required; everything else is optional.
- `--tag` is comma-separated (not repeatable for a single invocation); validate tags as lowercase alnum + dash.
- `--meta` repeatable, same parsing as `quest update --meta`.
- `--parent`: must be in `open` status (spec §Parent Tasks); depth-check — new depth = `Depth(parent)+1`, reject if > 3 with `ErrConflict` citing "depth exceeded".
- Dependency flags (`--blocked-by`, `--caused-by`, `--discovered-from`, `--retry-of`): validated in-process via the shared dep-validator (Task 7.2).
- Transaction: `BeginImmediate(TxKind=create_child)` when `--parent` is set, otherwise wrap in a standard transaction (still need atomicity for the counter increment + row insert). Generate ID inside the tx via `ids.NewTopLevel` or `ids.NewSubTask`.
- Append a `created` history row with a payload that captures non-default fields (tier, role, type, tags, dependencies). This is the retrospective input.

**Tests:** Layer 3 full matrix of precondition failures: parent-not-open (exit 5), depth exceeded (exit 5), missing title (exit 2), each dep-validation rule (see Task 7.2 for the table).

**Done when:** a create-one-chain test produces the expected tree and every dependency rule fires the correct exit code.

---

### Task 7.2 — Dependency validator (`internal/batch/deps.go`, shared with batch)

**Deliverable:** `deps.Validate(ctx, store, source TaskShape, edges []Edge) []DepError` — returns every violation, not just the first.

**Spec anchors:** `quest-spec.md` §Dependency validation (cycle detection for `blocked-by`, semantic constraints per link type), §Multi-type links.

**Implementation notes:**

- Validation lives here, not in the command handler, because `quest batch` and `quest create` both use it (and so will `quest link`).
- Cycle detection is only for `blocked-by`. Use iterative DFS over the existing dependency graph plus the in-flight edges. Cycle path is a `[]string` for error reporting.
- Semantic constraints (full table is in the spec):
  - `blocked-by` → target must not be `cancelled`.
  - `retry-of` → target must be `failed`.
  - `caused-by` → source must have `type=bug`.
  - `discovered-from` → source must have `type=bug`.
- Return errors with structured codes: `DepErrCode` enum (`cycle`, `blocked_by_cancelled`, `retry_target_status`, `source_type_required`, `unknown_task_id`). These codes feed both the CLI stderr JSONL and the OTEL events.

**Tests:** Layer 1 cycle detection on in-memory graphs; Layer 3 semantic checks against real tasks in the store.

**Done when:** `quest create`, `quest batch` (Task 7.3), and `quest link` (Task 9.1) all share this validator and pass the full constraint table.

---

### Task 7.3 — `quest batch FILE [--partial-ok]`

**Deliverable:** `internal/batch/` package (the four validation phases) + `internal/command/batch.go` (CLI glue).

**Spec anchors:** `quest-spec.md` §`quest batch` — full section including the error-code table, atomicity semantics, and the example JSONL output format.

**Implementation notes:**

- JSONL reader: one object per non-blank line. Blank lines (whitespace-only) are skipped without affecting line numbers per spec.
- Four phases, each a function that takes the batch and returns a `[]BatchError`. `BatchError` fields: `Line int`, `Phase string`, `Code string`, plus code-specific fields per the spec's "Extra fields" table.
- Cross-phase isolation: a line that failed an earlier phase is excluded from later phases — do not emit derived errors (e.g., "unresolved ref" because the referenced line was malformed). Track a `valid` bitmap across phases.
- Atomic mode (default): any error → zero tasks created, exit 2.
- `--partial-ok`: create the subset of lines that passed every phase AND whose references all resolve to created-or-existing tasks. Exit 2 even on partial success (the non-zero exit signals the planner that follow-up is needed). Emit the full ref→id mapping on stdout (JSONL) for created tasks; emit errors on stderr (JSONL).
- Validation prunes failure-dependent lines before creation begins (spec §Batch validation: "A line that fails an earlier phase is excluded from later-phase evaluation"), so the creation step only sees lines whose refs all resolve. Wrap creation in a single `BeginImmediate(TxKind=create_child)` transaction; if the transaction fails for a runtime reason (constraint violation, lock timeout, etc.), abort the whole tx and report cleanly.
- `ref` resolution: `ref` maps to an internal batch label; during creation, resolve to the actual generated ID. External `id` references are looked up in the store.
- `parent` accepts three input shapes per spec §Batch file format: a bare string (shorthand for `{"ref": "<s>"}`), `{"ref": "<s>"}`, or `{"id": "<s>"}`. The same object shape already applies to `dependencies[]` entries — factor the parse+validation into one `parseRef(raw json.RawMessage) (RefTarget, error)` helper used by both. `RefTarget` is a small struct with exactly-one-of `Ref string` / `ID string`; having both keys set or neither set returns the `ambiguous_reference` parse error (phase 1) with `field` set to `parent` or `dependencies[n]`. Unresolved `ref` → `unresolved_ref`; unknown `id` → `unknown_task_id` (both phase 2, unchanged from spec).

**Tests:** Layer 2 contract (every error code appears on stderr with the documented fields), Layer 3 (atomic vs partial, cross-phase isolation, the cycle/depth edge cases), Layer 1 for the pure parse/reference phases.

Fixtures in `internal/batch/testdata/` — see `TESTING.md` §Test Fixtures for the naming convention.

**Done when:** every error code in the spec's error-code table is covered by a fixture and a test.

---

## Phase 8 — Task management

### Task 8.1 — `quest cancel`

**Deliverable:** `internal/command/cancel.go`.

**Spec anchors:** `quest-spec.md` §`quest cancel` (with and without `-r`), §In-flight worker coordination.

**Implementation notes:**

- Without `-r`: `BeginImmediate(TxKind=cancel_recursive)` even for the single-task path, because the precondition check (no non-terminal children) is multi-row.
- With `-r`: recursive descendant walk; transition `open` and `accepted` descendants to `cancelled`; record skipped (already-terminal) descendants. Report both sets in the response.
- Idempotent on already-cancelled (exit 0). Rejects `complete` / `failed` (exit 5 — terminal states are permanent).
- `--reason` is optional; empty value records as `null` in history.
- History: `cancelled` with `reason` in the payload.
- Do not signal vigil or any external system; worker termination is out of scope per spec.

**Tests:** Layer 3: all four before-states, `-r` on a multi-level tree, idempotency on already-cancelled.

**Done when:** a cancelled task, when later `quest update`d by a worker, returns the structured conflict body per spec §In-flight worker coordination.

---

### Task 8.2 — `quest reset`

**Deliverable:** `internal/command/reset.go`.

**Spec anchors:** `quest-spec.md` §`quest reset`, §Crash Recovery.

**Implementation notes:**

- Route through `BeginImmediate` (reuse `TxKind=accept_parent` or introduce `TxKind=reset` — the shape is the same as accept: SELECT to distinguish not-found from wrong-status, then UPDATE). Do not use the atomic-UPDATE shortcut; same rationale as Task 6.2 (must distinguish exit 3 from exit 5).
- Missing task → exit 3. Task exists but not in `accepted` status → exit 5.
- On success: `UPDATE tasks SET status='open', owner_session=NULL, started_at=NULL WHERE id=?`. Preserve `handoff`, `handoff_session`, `handoff_written_at`, `notes` — the next session inherits them.
- `--reason` is optional; empty value records as `null` in history.
- History: `reset` with `reason` in the payload.

**Tests:** Layer 3: accepted → open + preserved handoff; non-accepted → exit 5.

**Done when:** the worker-crash test from `quest-spec.md` §Crash Recovery round-trips: accept, handoff, reset, re-accept by a new session, handoff visible on `show`.

---

### Task 8.3 — `quest move ID --parent NEW_PARENT`

**Deliverable:** `internal/command/move.go`.

**Spec anchors:** `quest-spec.md` §`quest move` — every constraint in the Constraints list.

**Implementation notes:**

- Hardest command. Read the spec twice before writing code.
- `BeginImmediate(TxKind=move)`. Preconditions (fail with exit 5, collecting all applicable messages):
  - The moved subgraph has no `accepted` action in history (for _any_ task in the subgraph, ever — check the history table, not the current status).
  - The moved task's current parent is not in `accepted` status.
  - `NEW_PARENT` is in `open` status.
  - No circular parentage: `NEW_PARENT` is not the moved task or any of its descendants.
  - The resulting depth of the deepest descendant ≤ 3.
- Rename algorithm: compute the new root ID via `NextSubTaskID(NEW_PARENT)`; for every descendant, derive the new ID by swapping the old prefix for the new. SQLite does not cascade ID renames (only DELETEs), so do explicit UPDATEs on `tasks.id`, `tasks.parent`, `dependencies.task_id`, `dependencies.target_id`, `tags.task_id`, `prs.task_id`, `notes.task_id`, `history.task_id`, and `subtask_counter.parent_id`. At the start of the move transaction, issue `PRAGMA defer_foreign_keys = ON` — this is the per-transaction escape hatch documented by SQLite; it defers FK enforcement until COMMIT and resets automatically on the next COMMIT/ROLLBACK. (Note: `PRAGMA foreign_keys = OFF` is a no-op inside a transaction and is _not_ the right mechanism; an earlier plan draft had this wrong.) Re-assert `defer_foreign_keys = ON` at the start of every move transaction — it does not persist across transactions.
- Append one `moved` history entry per renamed task with `old_id` / `new_id` in the payload. Updates to dependency references are not their own history entries.
- Output: JSON mapping of old→new IDs.

**Tests:** Layer 3: the full constraint list; subgraph rename round-trip; ID uniqueness after move.

**Done when:** a 3-level subgraph with dependencies moves cleanly and `quest show` on every affected task reflects the new IDs.

---

## Phase 9 — Links and tags

### Task 9.1 — `quest link` and `quest unlink`

**Deliverable:** `internal/command/link.go`, `internal/command/unlink.go`.

**Spec anchors:** `quest-spec.md` §Linking, §Multi-type links (uniqueness on `(task, target, type)`), §Dependency validation.

**Implementation notes:**

- `link`: route through the dep-validator (Task 7.2) with `op=add`. Idempotent on duplicate (task, target, type). `INSERT OR IGNORE` + check RowsAffected.
- `unlink`: `DELETE FROM dependencies WHERE task_id=? AND target_id=? AND link_type=?`. Idempotent on missing row.
- History: `linked` / `unlinked` with `target` and `link_type` in the payload.
- Default relationship is `--blocked-by` when no flag is provided.

**Tests:** Layer 3: each link type, cycle on add (exit 5), duplicate-add no-op, unlink no-op.

**Done when:** all four link types round-trip through link→show→unlink cleanly.

---

### Task 9.2 — `quest tag` and `quest untag`

**Deliverable:** `internal/command/tag.go`, `internal/command/untag.go`.

**Spec anchors:** `quest-spec.md` §Tags.

**Implementation notes:**

- Tags are comma-separated on the command line, normalized to lowercase, stored lowercase.
- `INSERT OR IGNORE` for add, `DELETE` for remove. Both idempotent.
- History: `tagged` / `untagged` with the tag list in the payload.

**Tests:** Layer 3 add + remove + idempotency.

**Done when:** tag management round-trips cleanly; `--tag` filter in `quest list` (Phase 10) matches what `tag` writes.

---

## Phase 10 — Queries

### Task 10.1 — `quest deps`

**Deliverable:** `internal/command/deps.go`.

**Spec anchors:** `quest-spec.md` §Queries §`quest deps`.

**Implementation notes:**

- Unlike worker commands, `deps` does not default to `AGENT_TASK`. Require an explicit ID; missing → `ErrUsage`.
- Return dependencies with title and status denormalized (same shape as the `dependencies` array on `quest show`).

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
- `--ready` has the trickiest semantics per spec:
  - Leaves: `status == open` AND every `blocked-by` target is `complete`.
  - Parents: `status == open` AND every `blocked-by` target is `complete` AND every child is terminal.
  - Mix leaves and parents in a single response; the presence of `children` tells the caller which is which.
- Column selection: `--columns` overrides defaults (`id`, `status`, `blocked-by`, `title`).
- JSON output is an array, not JSONL — `list` is a bounded result set.

**Tests:** Layer 3 matrix — every flag combination has at least one case. `--ready` has its own test covering leaf-ready, leaf-blocked, parent-ready-roleful (dispatch), parent-ready-roleless (direct-close), parent-not-ready (non-terminal children).

---

### Task 10.3 — `quest graph ID`

**Deliverable:** `internal/command/graph.go`.

**Spec anchors:** `quest-spec.md` §Queries §`quest graph` — full JSON shape and "external nodes" semantics.

**Implementation notes:**

- Traverse from `ID` through `children` (parent-child) and follow outgoing dependency edges.
- Any target reached via a dependency edge that is _not_ a descendant of `ID` is an **external** node: it appears in `nodes` with `children: []` and its own edges are not expanded. Consumers detect it via ID prefix comparison.
- `edges[]` uses quest-specific field names (`task`, `target`, `type`, `target_status`), not generic `source`/`target`.
- Text mode: indented tree per the spec example, with dependency edges listed under the owning task.

**Tests:** Layer 3: root at epic (full tree); root at leaf (just the leaf + external deps); dep-only cross-prefix external; depth-limited traversal correctness.

---

## Phase 11 — Export

### Task 11.1 — `quest export [--dir PATH]`

**Deliverable:** `internal/export/` package + `internal/command/export.go`.

**Spec anchors:** `quest-spec.md` §`quest export` (layout, idempotency).

**Implementation notes:**

- Default output: `./quest-export/` (a sibling of `.quest/`).
- Layout exactly as specified: `tasks/{id}.json` for every task, `debriefs/{id}.md` only for tasks that have a non-empty debrief, `history.jsonl` chronologically across all tasks.
- Task JSON uses the same shape as `quest show --history` (i.e., includes the full history array). Contract test asserts this equivalence.
- Idempotent: re-running overwrites. Safe pattern: write each file with a temp suffix and `os.Rename` atomically, then remove temp entries at the end; OR remove the directory first (agent UX: confirm or allow `--dir` to be a non-existing path). The spec says "overwrites the output directory" — interpret as "makes the output directory reflect current state," which means old files for deleted tasks should be removed. Track the set of task IDs written and delete any `tasks/*.json` / `debriefs/*.md` not in the set.
- `history.jsonl` entries: one JSON object per history row, ordered by timestamp ascending across all tasks.

**Tests:** Layer 2 contract: layout matches; task JSON field-for-field matches `quest show --history`. Layer 3: idempotency (run twice, diff the tree — should be byte-identical).

**Done when:** export round-trips the full database and produces files that are human-readable and diff-friendly.

---

## Phase 12 — Telemetry (OTEL)

Follow `OTEL.md` §16 "Implementation Sequence" — it is the canonical order. Each task below corresponds to one numbered item in §16.

### Task 12.1 — Real `telemetry.Setup` (tracer/meter/logger providers, fan-out slog bridge)

**Spec anchors:** `OTEL.md` §7 (full section), §8.1, §10.1–10.3, §11.

**Implementation notes:**

- Replace the no-op shell from Task 2.3 with real SDK wiring. Conditional: disabled → install explicit no-op providers.
- Service name `quest-cli`, resource via `semconv/v1.40.0`.
- Batch span/log processors with `WithBatchTimeout(1 * time.Second)`. Never `SimpleSpanProcessor`.
- Register the W3C composite propagator + `otel.SetErrorHandler` routing to slog.
- Partial-init cleanup per §7.8.
- `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` → slog warn + HTTP fallback.
- Install the `otelslog` bridge as the second child of the logging fan-out. OTEL-level filter defaults to `info`, stderr level default stays `warn`.

**Tests:** Layer 1: disabled path returns no-op providers; partial-init failure shuts down earlier providers; protocol warn fires once.

---

### Task 12.2 — `CommandSpan` + wire into every handler

**Spec anchors:** `OTEL.md` §4.2, §4.3, §8.2.

**Implementation notes:**

- Cache `AGENT_ROLE`/`AGENT_TASK`/`AGENT_SESSION` once via `sync.Once` in `internal/telemetry/env.go`.
- Two call shapes, both defined in `internal/telemetry/command.go`:
  - Primitive: `ctx, span := telemetry.CommandSpan(ctx, "accept", elevated); defer span.End()` — handler performs the §4.4 three-step error pattern (`RecordError` + `SetStatus` + `dept.quest.errors` counter) at each error exit, typically via a small `telemetry.RecordCommandError(ctx, span, err)` helper that reads `errors.ExitCode(err)` for the class attribute.
  - Wrapper: `return telemetry.WrapCommand(ctx, "accept", elevated, func(ctx context.Context) error { ... })` — the wrapper owns `span.End()` and the three-step pattern for whatever error `fn` returns. Prefer this for handlers that have a single linear flow; drop down to the primitive when the handler sets attributes mid-flow or records distinct exit codes from multiple branches.
- Root span name `execute_tool quest.<command>`; required attributes per §4.3. Both call shapes produce identical span content — the wrapper is sugar, not a second instrumentation point.

**Tests:** Layer 1 with in-memory exporter from `sdk/trace/tracetest`: assert root span name, `gen_ai.*` attributes present, `quest.role.elevated` bool.

---

### Task 12.3 — Role gate span + handler instrumentation

**Spec anchors:** `OTEL.md` §8.7.

**Implementation notes:** `quest.role.gate` child span around the gate check. Records `quest.role.required`, `quest.role.actual`, `quest.role.allowed`. Independent of whether the command proceeds.

---

### Task 12.4 — `InstrumentedStore` decorator

**Spec anchors:** `OTEL.md` §4.2 (span events, not spans, for uniform DML), §8.3, §4.3 (DB span attributes).

**Implementation notes:**

- Wrap the store. Per-operation: measure duration, call `span.AddEvent("quest.store.op", ...)` with `db.system=sqlite`, `db.operation`, `db.target`, `rows_affected`, `duration_ms`.
- `StoreTx` helper for `BEGIN IMMEDIATE` paths — separate span `quest.store.tx` with `quest.tx.lock_wait_ms`, `quest.tx.rows_affected`, `quest.tx.outcome`.
- `quest.store.traverse` around graph/list queries; `quest.store.rename_subgraph` around `move`.

**Tests:** Layer 1 + Layer 3 with the in-memory exporter: `accept` on a parent produces exactly one `quest.store.tx` span with correct attributes; `quest show` produces the expected events.

---

### Task 12.5 — Metrics (`dept.quest.*`)

**Spec anchors:** `OTEL.md` §5 (full section).

**Implementation notes:** Create every instrument listed in §5.1 at package init inside `internal/telemetry/recorder.go`. Wire increments through the `RecordX` functions that handlers already call (installed as no-ops in Task 2.3). Histogram bucket boundaries per §5.2.

**Tests:** Instrument-creation test per `OTEL.md` §14.4; exit-code-to-class coverage test per §14.6.

---

### Task 12.6 — Batch validation spans + content capture + migration span

Three small wrap-up tasks covering `OTEL.md` §8.5, §8.8, §4.5 (content). Each is mechanical once the foundations are in place.

---

## Phase 13 — Contract, concurrency, and CI tests

### Task 13.1 — Contract test suite (Layer 2)

Per `TESTING.md` §Layer 2, implement at minimum:

- `TestShowJSONHasRequiredFields` — every field from spec §Task Entity Schema.
- `TestExitCodeStability` — the table from `STANDARDS.md` §CLI Output Contract Tests.
- `TestIdempotencyGuarantees` — every row of the spec's idempotency table.
- `TestHistoryEntryShape` — per-action required fields.
- `TestBatchStderrShape` — every batch error code with the documented fields.
- `TestExportLayout` — `tasks/{id}.json`, `debriefs/{id}.md`, `history.jsonl`.
- `TestRoleGateDenials` — every elevated command returns exit 6 for worker role.

File location: `internal/cli/contract_test.go` (CLI surface) + `internal/batch/contract_test.go` (batch).

**Done when:** all contract tests pass and any future spec-breaking change trips at least one.

---

### Task 13.2 — Concurrency tests (Layer 5)

Per `TESTING.md` §Layer 5 and `OTEL.md` §15:

- `TestConcurrentAcceptLeavesOnlyOneWinner` (accept race).
- `TestConcurrentCreateGeneratesDistinctIDs` (counter race).
- `TestBusyTimeoutTransientFailure` (5s lock wait, exit 7).
- `TestBulkBatchValidatesInReasonableTime` (500 tasks, dense blocked-by graph — soft perf target).

All behind `//go:build integration` and run with `-race`.

---

### Task 13.3 — CLI output contract tests (Layer 4)

Build the binary once in `TestMain` (`TESTING.md` §Store Fixtures and Seed Helpers), invoke it via `os/exec` for scenarios that can only be tested end-to-end: global flag positioning, `--format text` rendering, stderr `quest: <class>: <msg>` + `quest: exit N (<class>)` tail, `@file` input.

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

Every mutation writes exactly one history row. Never skip; never batch. The `action` enum and action-specific payload shape are defined in `quest-spec.md` §History field — implement once in `store.AppendHistory(ctx, tx, History) error` and call it from every write path.

### JSON field presence

Every struct that marshals to command output uses explicit `json:"..."` tags and emits `null` / `[]` / `{}` for empty values — never omit. Add a contract test for every command output; the set is non-negotiable per `STANDARDS.md` §CLI Surface Versioning.

### Error messages

User-facing stderr lines: `quest: <class>: <actionable message>` followed by `quest: exit N (<class>)`. The slog record carries the wrapped error. Never leak SQL, file paths from internal sources, or type names to stderr. See `OBSERVABILITY.md` §Sanitization.

### `@file` input

Any flag listed in `quest-spec.md` §Input Conventions goes through the shared `input.Resolve`. Adding new flags that accept free-form text? Add them to the list and the resolver handles them automatically.

### Telemetry call sites

Every command handler: `CommandSpan` at entry; `RecordX` at every observable event (status transition, link add/remove, batch outcome, query result count). The no-op stubs make these calls safe during Phase 2–11; Phase 12 lights them up. Do not gate calls on `telemetry.Enabled()` — the stubs / no-op SDK providers handle that cheaply.

### Schema evolution

Any change to the DB shape is a numbered migration. Bump `schema_version`. Add a `migration_test.go` fixture at the new version. Never edit an existing migration — the binary's supported-version set is forward-only.

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

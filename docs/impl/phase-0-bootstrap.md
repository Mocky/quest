# Phase 0 — Repository bootstrap

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

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

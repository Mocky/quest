# Phase 1 — Configuration

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

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

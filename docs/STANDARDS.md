# Project Standards

This document defines conventions for configuration management and CLI/API versioning. It complements `OBSERVABILITY.md`, `OTEL.md`, and `TESTING.md` and follows the same format: binding rules for both human developers and coding agents.

If you are a coding agent, treat every MUST/MUST NOT as a hard constraint.

Quest's canonical behavioral contract is `quest-spec.md`. This document governs how that contract is implemented; when the two conflict, the spec wins and this doc gets updated.

---

## Part 1 — Configuration Management

### Philosophy

All runtime behavior that varies between environments, machines, or users is configuration. Configuration is loaded once at startup, from a single source of truth, through a single package. Nothing else in the codebase reads the environment, parses flags, or opens config files directly.

Quest has three configuration surfaces, in decreasing order of volatility:

- **Command-line flags.** Per-invocation overrides. Highest precedence.
- **Environment variables (`QUEST_*` and the agent context vars).** Per-session defaults set by vigil or the operator.
- **Project config file (`.quest/config.toml`).** Per-project settings fixed at `quest init` (e.g., `id_prefix`, `elevated_roles`).

The config file is not user-editable config in the usual sense — it is quest's workspace marker and holds settings that are immutable for the project's life. Treat it the same way as any other config input: read it through `internal/config/`, never directly.

### The Config Package (`internal/config/`)

All configuration flows through `internal/config/`. This package is responsible for:

- Defining the config struct with all fields, defaults, and validation
- Walking up from CWD to locate `.quest/` and reading `.quest/config.toml`
- Loading values from environment variables
- Loading values from command-line flags
- Validating the final config and returning clear errors for invalid combinations

No other package may call `os.Getenv`, `os.LookupEnv`, `flag.Parse`, or read `.quest/config.toml` directly. If a package needs a config value, it receives it as an explicit parameter — either the full config struct or the specific value it needs.

The agent context variables (`AGENT_ROLE`, `AGENT_TASK`, `AGENT_SESSION`, `TRACEPARENT`, `TRACESTATE`) are read by `internal/config/` at startup and surfaced as typed fields on the config struct. Packages that need them (role gating, telemetry, history entries) receive the resolved values, not the env vars.

### Config Struct

Define a single top-level `Config` struct. Nest related fields:

```go
// Config holds all resolved configuration for a quest invocation.
type Config struct {
    Workspace WorkspaceConfig
    Agent     AgentConfig
    Log       LogConfig
    Telemetry TelemetryConfig
}

type WorkspaceConfig struct {
    // Root is the absolute path to the directory containing .quest/.
    // Discovered by walking up from CWD.
    // Required — if no .quest/ is found, quest exits with code 2.
    Root string

    // IDPrefix is the project-specific task ID prefix (e.g., "proj").
    // Source: .quest/config.toml (set once at quest init).
    // Required. Validated per quest-spec Prefix validation rules.
    IDPrefix string

    // ElevatedRoles is the set of AGENT_ROLE values that unlock the planner surface.
    // Source: .quest/config.toml. Default: [].
    ElevatedRoles []string
}

type AgentConfig struct {
    // Role is the calling agent's role, from AGENT_ROLE. Empty string when unset.
    // Env: AGENT_ROLE
    Role string

    // Task is the agent's assigned task ID, from AGENT_TASK. Empty string when unset.
    // Env: AGENT_TASK
    Task string

    // Session is the vigil-assigned session ID, from AGENT_SESSION. Empty when unset.
    // Env: AGENT_SESSION
    Session string
}

type LogConfig struct {
    // Level is the minimum slog level for stderr output.
    // Default: "warn"
    // Env: QUEST_LOG_LEVEL
    // Flag: --log-level
    Level string

    // OTELLevel is the minimum slog level forwarded to the OTEL log bridge.
    // Default: "info"
    // Env: QUEST_LOG_OTEL_LEVEL
    OTELLevel string
}

type TelemetryConfig struct {
    // CaptureContent enables free-form content capture in span events.
    // Default: false
    // Env: OTEL_GENAI_CAPTURE_CONTENT (shared with the rest of grove)
    CaptureContent bool

    // Standard OTEL_* variables are read by the OTEL SDK directly; quest does
    // not re-expose them as Config fields. See OTEL.md §7 for the full list.
}
```

Keep the struct small. Quest has few legitimate configuration points — the system's behavior is driven by task data and commands, not by knobs.

### Environment Variable Naming

Quest-specific variables use the `QUEST_` prefix followed by uppercase snake_case:

```
QUEST_LOG_LEVEL
QUEST_LOG_OTEL_LEVEL
```

Cross-grove and framework variables keep their existing names — they are contracts with vigil, the OTEL SDK, and other tools, not quest-local configuration:

```
AGENT_ROLE
AGENT_TASK
AGENT_SESSION
TRACEPARENT
TRACESTATE
OTEL_EXPORTER_OTLP_ENDPOINT
OTEL_GENAI_CAPTURE_CONTENT
...
```

Rules:

- The `QUEST_` prefix is always present for quest-local variables. No bare `LOG_LEVEL`.
- Names match the config struct field path: `LogConfig.Level` → `QUEST_LOG_LEVEL`. Keep this mapping predictable.
- Boolean variables use the field name without `IS_` or `ENABLE_` prefixes. Parsed with `strconv.ParseBool`, which accepts `1`, `t`, `T`, `TRUE`, `true`, `True`, `0`, `f`, `F`, `FALSE`, `false`, `False` — and nothing else. Do not document `yes`/`y`/`on` as accepted; they are not.
- Do not add a `QUEST_` variable that duplicates a `.quest/config.toml` field. Config-file settings are immutable for the project; environment overrides would silently split the source of truth.

### Config File (`.quest/config.toml`)

Written once by `quest init`, read on every invocation. Fields are immutable for the project's lifetime. Shape per the spec:

```toml
# .quest/config.toml

# Role gating — AGENT_ROLE values that unlock elevated commands.
elevated_roles = ["planner"]

# Task IDs
id_prefix = "proj"
```

Rules:

- Every config file field has a validation rule in `internal/config/` and a clear error message citing the file path and field name when it fails.
- Unknown fields produce a warning (slog at `WARN`), not an error — forward compatibility matters, since the config file may be written by a newer quest and read by an older one during a downgrade attempt.
- Do not add fields to `config.toml` that should reasonably change over the project's life (log level, debug flags, tunables). Those belong on flags or environment variables.
- Adding a new field to `config.toml` is a schema change. Bump the schema understanding: old binaries must tolerate reading the new field (forward compatibility) and new binaries must tolerate its absence in projects initialized before the field existed (default value).

### Defaults

Every config field either has a sensible default or is explicitly required. Document both. Example:

```go
// Load reads configuration from config.toml, environment variables, and flags.
func Load(flags Flags) (Config, error) {
    root, err := discoverWorkspace()
    if err != nil {
        return Config{}, err
    }

    file, err := readConfigFile(root)
    if err != nil {
        return Config{}, err
    }

    cfg := Config{
        Workspace: WorkspaceConfig{
            Root:          root,
            IDPrefix:      file.IDPrefix,         // required, no default
            ElevatedRoles: file.ElevatedRoles,    // default: nil (empty)
        },
        Agent: AgentConfig{
            Role:    os.Getenv("AGENT_ROLE"),    // empty when unset
            Task:    os.Getenv("AGENT_TASK"),
            Session: os.Getenv("AGENT_SESSION"),
        },
        Log: LogConfig{
            Level:     firstNonEmpty(flags.LogLevel, os.Getenv("QUEST_LOG_LEVEL"), "warn"),
            OTELLevel: firstNonEmpty(os.Getenv("QUEST_LOG_OTEL_LEVEL"), "info"),
        },
        Telemetry: TelemetryConfig{
            CaptureContent: envBool("OTEL_GENAI_CAPTURE_CONTENT"),
        },
    }

    return cfg, cfg.Validate()
}
```

Required fields with no default MUST cause a clear startup error with the missing source:

```
error: .quest/config.toml: id_prefix is required
```

Not:

```
error: id prefix is empty
```

The error names what the operator needs to fix and where.

### Flag Overrides (per-invocation)

Quest supports a small set of global flags that override environment variables and config-file values:

```
flag > environment variable > .quest/config.toml > default
```

Flag names use kebab-case and mirror the environment variable name without the `QUEST_` prefix:

```
QUEST_LOG_LEVEL  → --log-level
--format (no env equivalent, defaults to "json")
--color  (no env equivalent, off by default; TTY-dependent in text mode)
```

Global flags (`--format`, `--log-level`, `--color`) MUST be position-independent — they can appear before or after the command name. The CLI extracts them in a first parsing pass before dispatching the command.

`.quest/config.toml` fields do not get flag overrides. They are immutable for the life of the project; the only way to change `id_prefix` or `elevated_roles` is to edit the file directly.

### Validation

The `Validate()` method on `Config` checks constraints and returns all violations, not just the first one:

```go
func (c Config) Validate() error {
    var errs []string

    if c.Workspace.IDPrefix == "" {
        errs = append(errs, ".quest/config.toml: id_prefix is required")
    } else if !validPrefix(c.Workspace.IDPrefix) {
        errs = append(errs,
            fmt.Sprintf(".quest/config.toml: id_prefix %q must match ^[a-z][a-z0-9]{1,7}$",
                c.Workspace.IDPrefix))
    }

    if _, ok := parseLevel(c.Log.Level); !ok {
        errs = append(errs,
            fmt.Sprintf("QUEST_LOG_LEVEL: %q is not a valid log level", c.Log.Level))
    }

    if len(errs) > 0 {
        return fmt.Errorf("configuration errors:\n  %s", strings.Join(errs, "\n  "))
    }
    return nil
}
```

Validation error messages name the source (`.quest/config.toml:`, `QUEST_LOG_LEVEL:`, `--log-level:`) so the operator knows what to fix without guessing.

### What Must Never Be Config

Some values are constants, not configuration. Do not make them configurable:

- Exit codes (1-7) and their semantics
- Error class strings (`general_failure`, `usage_error`, `not_found`, `permission_denied`, `conflict`, `role_denied`, `transient_failure`)
- JSON field names in command output (`id`, `status`, `dependencies`, etc.)
- Task ID format and separators (`-`, `.`)
- SQLite pragmas required for correctness (`busy_timeout = 5000`, `journal_mode = WAL`)
- Structural thresholds defined by the spec (maximum nesting depth of 3, 5-second lock timeout)
- Schema version numbers
- Build-time metadata (version string, commit hash — set via `-ldflags`)

If a value has exactly one correct answer, it is a constant, not a config field. The busy-timeout threshold is the clearest example: it is the exit-code-7 contract with callers and the daemon-upgrade signal. Making it configurable would break that contract silently.

### Config in Tests

Tests MUST NOT depend on the calling process's environment. When a function needs a config value, pass it explicitly:

```go
// CORRECT — test controls the value
func TestAccept(t *testing.T) {
    cfg := config.Config{
        Workspace: config.WorkspaceConfig{Root: t.TempDir(), IDPrefix: "tst"},
        Agent:     config.AgentConfig{Role: "coder", Task: "tst-01", Session: "sess-a"},
    }
    // ...
}

// WRONG — test depends on external state
func TestAccept(t *testing.T) {
    os.Setenv("AGENT_ROLE", "coder") // pollutes process environment
    cfg, _ := config.Load(config.Flags{})
    // ...
}
```

If you must test the `Load()` function itself (to verify env var or flag parsing), use `t.Setenv()` which automatically restores the original value when the test finishes.

For tests that need a quest workspace on disk, use `t.TempDir()` and a helper that writes a minimal `.quest/config.toml`. Do not share workspaces across tests; each test owns its own.

### Anti-Patterns

#### 1. Scattered environment reads

```go
// WRONG — os.Getenv outside the config package
func (h *Handler) handleAccept(...) {
    if os.Getenv("AGENT_ROLE") == "planner" {
        // ...
    }
}

// CORRECT — config value passed in at construction
func NewHandler(cfg config.Config) *Handler {
    return &Handler{elevated: isElevated(cfg.Agent.Role, cfg.Workspace.ElevatedRoles)}
}
```

#### 2. Direct config-file reads

```go
// WRONG — parsing .quest/config.toml outside internal/config
data, _ := os.ReadFile(".quest/config.toml")

// CORRECT — Config struct is already populated
cfg.Workspace.IDPrefix
```

#### 3. Hardcoded values that should be config

```go
// WRONG
db := sql.Open("sqlite", ".quest/quest.db")

// CORRECT
db := sql.Open("sqlite", filepath.Join(cfg.Workspace.Root, ".quest/quest.db"))
```

#### 4. Hardcoded values that should be constants

```go
// WRONG — making a structural invariant configurable adds complexity with no benefit
var maxDepth = envIntOr("QUEST_MAX_DEPTH", 3)

// CORRECT — this has one right answer per the spec
const MaxDepth = 3
```

#### 5. Config fields without documentation

```go
// WRONG — what is this? what's the default? where does it come from?
type LogConfig struct {
    Level string
}

// CORRECT
type LogConfig struct {
    // Level is the minimum slog level for stderr output.
    // Default: "warn"
    // Env: QUEST_LOG_LEVEL
    // Flag: --log-level
    Level string
}
```

#### 6. Validation that stops at the first error

```go
// WRONG — operator fixes one thing, restarts, hits the next error
if cfg.Workspace.IDPrefix == "" {
    return fmt.Errorf("id_prefix is required")
}
if cfg.Log.Level == "" {
    return fmt.Errorf("log level is required")
}

// CORRECT — report all problems at once
var errs []string
if cfg.Workspace.IDPrefix == "" {
    errs = append(errs, ".quest/config.toml: id_prefix is required")
}
if cfg.Log.Level == "" {
    errs = append(errs, "QUEST_LOG_LEVEL: log level is required")
}
```

---

## Part 2 — CLI Surface Versioning and Backward Compatibility

### What Is the API

For quest, "the API" is the agent-callable surface. It has four layers, all of which are contracts:

1. **Command and flag names** — `quest accept`, `quest batch --partial-ok`, `--format json`.
2. **JSON output on stdout** — field names, types, and presence guarantees (see spec: "All fields are always present in JSON output").
3. **Exit codes (1-7)** — defined in the spec's Output & Error Conventions and mapped 1:1 to OTEL error classes.
4. **Environment variables quest reads** — `AGENT_ROLE`, `AGENT_TASK`, `AGENT_SESSION`, `TRACEPARENT`, `QUEST_*`.

Text-mode output (`--format text`) is a human-friendly rendering, not a contract. Its shape can change between versions; agents must not parse it.

History entries and export format are also contracts — retrospective tooling depends on them. Treat them like JSON output.

### Versioning Scheme

Quest has two version numbers:

- **Binary version (`quest version`).** SemVer-ish string set at build time. Informational, for humans and telemetry attributes.
- **Database schema version (`meta.schema_version` in `quest.db`).** Integer starting at 1, incremented on every backward-incompatible schema change. Already specified in the quest-spec Storage section.

Quest's JSON output and CLI surface do not carry a per-request version field. The wire format is "whatever this binary emits" — there is no client/server split the way lore has. Backward compatibility is enforced at the schema level (for persisted data) and by these rules (for the agent-facing surface).

If quest later ships a `questd` daemon, that daemon WILL carry a protocol version field on every request, and these rules will extend to cover it. Until then, `schema_version` is the only version number and the CLI surface is governed by the compatibility rules below.

### Compatibility Rules

#### Always Safe (no compatibility concern)

- **Adding a new command.** Existing commands are unaffected. Agents that don't know the new command simply don't use it.
- **Adding a new optional flag** to an existing command. Default value preserves prior behavior.
- **Adding a new field** to JSON output. Agents MUST ignore fields they do not recognize; the spec already requires this via the "all fields always present" rule, which is about additive changes over time.
- **Adding a new history action** to the `action` enum (e.g., a new mutation type). Agents SHOULD handle unknown actions gracefully.
- **Adding a new error code (exit code slot).** Exit codes 1-7 are stable; if we ever add 8, agents that don't recognize it should treat it as a generic failure. This is unlikely to happen — bend toward reusing existing codes.
- **Changing a stderr message** on an error. Stderr text is for humans. Agents MUST NOT parse it for control flow.
- **Adding a new schema version with a forward-only migration.** The database upgrades in place. See quest-spec Storage.

#### Requires Careful Handling (breaking)

- **Removing a field** from JSON output that agents depend on.
- **Renaming a field** in JSON output (equivalent to removing the old one and adding a new one).
- **Changing a field's type** (e.g., `"tier": "T2"` → `"tier": 2`).
- **Changing the meaning of an existing field** (e.g., `tier` was a string and is now an integer index).
- **Removing a command or a non-optional flag.**
- **Making a previously optional flag required** on an existing command.
- **Changing an exit code's meaning** (e.g., what used to be exit 5 now means exit 6). The exit-code-to-error-class mapping is a load-bearing contract — see OTEL.md §4.4.
- **Changing an idempotency contract** (e.g., making `quest tag` no longer idempotent when adding an already-present tag). The spec's idempotency table is a documented contract; agents build retry logic around it.
- **Changing a history entry's shape** (adding a required field, renaming an existing field, changing a value type).
- **Changing export file layout or contents** (filename pattern, directory structure, JSON shape).

Breaking changes MUST:

1. Be noted in `CHANGELOG.md` under **Changed** or **Removed** with migration guidance.
2. Ship a schema version bump if persisted data changes.
3. Be coordinated with any dependent tools (rite, vigil, curator playbooks) — the change is not "done" until those callers are updated.

#### Deprecation Process

When a field, flag, command, or behavior needs to be removed:

1. **Announce deprecation.** Add a changelog entry under **Deprecated** with the release that will remove it. The feature continues to work normally.
2. **Log a slog `WARN`** when the deprecated feature is used. This is a side channel; agents ignore stderr for control flow, but operators and log reviewers see it.
3. **Remove the feature** in the announced release, and bump the schema version if persisted data is affected.
4. **Document the removal** in `CHANGELOG.md` under **Removed**, with a pointer to the replacement if one exists.

For JSON output fields specifically, the graceful path is "keep the old field populated alongside the new one until the next major version, even if the value is computed." Renames are cheaper than removals for this reason.

### Schema Migration Rules

The `meta.schema_version` integer in `quest.db` is the only durable version number. Rules (already in the spec, restated here for completeness):

- Every schema change ships a numbered forward-only migration bundled into the binary.
- Migrations run in a single transaction; failure leaves the database at the prior version.
- The binary refuses to operate against a database with `schema_version` higher than it supports (exit code 1, stderr: `"database schema version N is newer than this binary supports — upgrade quest"`).
- Downgrades are not supported. The recovery path is restore from `quest export` or a file-level backup.

Implementers: keep migrations pure SQL where possible. Go-coded migrations (for data transforms) are allowed but must be testable against a fixture database from every prior version — see TESTING.md on migration tests.

### Exit Code Stability

The exit codes defined in the spec (1-7) map 1:1 to `quest.error.class` vocabulary in OTEL.md §4.4. Changing a code is a breaking change to both the CLI surface and the telemetry contract. Do not renumber. Do not repurpose. If a new class is needed, add a new code — ideally 8 — and add it to the OTEL class map in lockstep.

### CLI Output Contract Tests

Contract tests live in the CLI output test layer (see TESTING.md). They serve as tripwires for breaking changes:

```go
// TestShowJSONHasRequiredFields documents the fields agents depend on.
// If this test fails, you are about to break agents.
// Either restore the field or announce the removal in the changelog and bump
// the schema version if relevant.
func TestShowJSONHasRequiredFields(t *testing.T) {
    // Invoke `quest show` against a fixture DB, parse stdout as JSON,
    // assert that every field listed below is present.
    required := []string{
        "id", "title", "description", "context", "type", "status",
        "role", "tier", "tags", "parent", "acceptance_criteria", "metadata",
        "owner_session", "started_at", "completed_at",
        "dependencies", "prs", "notes",
        "handoff", "handoff_session", "handoff_written_at",
        "debrief",
    }
    // ...
}

// TestExitCodeStability pins the exit-code-to-error-class mapping.
func TestExitCodeStability(t *testing.T) {
    cases := []struct{ code int; class string }{
        {1, "general_failure"},
        {2, "usage_error"},
        {3, "not_found"},
        {4, "permission_denied"},
        {5, "conflict"},
        {6, "role_denied"},
        {7, "transient_failure"},
    }
    // ...
}
```

If one of these tests fails, do not "fix the test to match the code." Revert the change or file a breaking-change PR with changelog and schema-version updates.

### Changelog

Maintain a `CHANGELOG.md` in the repo root. Every user-visible change gets an entry. Follow Keep a Changelog (https://keepachangelog.com/):

```markdown
# Changelog

## [Unreleased]

### Added
- `quest list --ready` filter — returns tasks whose next transition has no unmet preconditions.
- `handoff_written_at` field on `quest show` JSON output.

### Changed
- Default log level lowered from `info` to `warn` to match lore.

### Deprecated
- `quest show --json` alias for `--format json`. Removed in v0.3.

### Removed
- Nothing in this release.

### Fixed
- `quest batch` no longer double-counts blank lines when reporting error line numbers.

### Schema
- `schema_version` 1 → 2: added `meta.capture_content_default` (nullable, no data migration).
```

Agents SHOULD add changelog entries when they make user-visible changes. Include this in your commit message template or PR checklist.

### Anti-Patterns

#### 1. Renaming a JSON field without a deprecation cycle

```go
// WRONG — breaks every agent that reads "dependencies"
// Before:
type TaskJSON struct {
    Dependencies []DepJSON `json:"dependencies"`
}
// After (someone "cleaned up" the name):
type TaskJSON struct {
    Deps []DepJSON `json:"deps"`
}

// CORRECT — add the new name, keep the old one, log a deprecation warning
type TaskJSON struct {
    Dependencies []DepJSON `json:"dependencies"`
    // Do not add an alias; agents already read "dependencies". If you MUST
    // rename, populate both fields for one release, log WARN on writes, and
    // remove the old name in the next major bump with a changelog entry.
}
```

#### 2. Changing a field's type

```go
// WRONG — was a string, now it's an int
// Before: "tier": "T2"
// After:  "tier": 2

// CORRECT — tiers are strings ("T0".."T6") per the spec. If a new analytical
// shape is needed, add a new field (e.g., "tier_rank": 2) and keep "tier" as-is.
```

#### 3. Making a previously optional flag required

```go
// WRONG — agents that don't pass --format now fail
// quest show now requires --format

// CORRECT — the default stays "json"; agents that omit --format keep working.
```

#### 4. Repurposing an exit code

```go
// WRONG — exit 5 used to be "conflict"; now it's "usage error"
// Breaks every dashboard, every OTEL error-class map, every agent retry heuristic.

// CORRECT — exit codes are permanent. Add exit 8 (or reuse 2) if needed.
```

#### 5. Silent schema mutation

```go
// WRONG — add a column to the tasks table without bumping schema_version
ALTER TABLE tasks ADD COLUMN priority INTEGER;
// Older binaries now see an unexpected column; newer ones assume it's always there.

// CORRECT — add a numbered migration, bump schema_version, and ship the new
// binary that writes and reads the column. Older binaries refuse the new version
// (exit 1), which is the intended fallback.
```

#### 6. No changelog entry

```go
// WRONG — ship a change that agents depend on with no record of it
// "cleaned up show output" — but nobody knows "debrief_size" is now "debrief_bytes"

// CORRECT — every agent-visible change gets a changelog entry with the precise field name.
```

---

## Agent Instructions Summary

### Configuration

1. **Never call `os.Getenv` or `flag.Parse` outside `internal/config/`.** If you need a value, accept it as a parameter.
2. **Never hardcode** workspace paths, task ID prefixes, log levels, or session IDs. These are config values.
3. **Do hardcode** exit codes, error class strings, JSON field names, structural limits (depth 3, busy timeout 5s), and SQLite pragmas. These are not config.
4. **Every new config field** gets a doc comment with: description, default value, environment variable (if any), flag name (if any), and source file (for config.toml fields).
5. **All `QUEST_*` environment variables** use `UPPER_SNAKE_CASE`. Reserve the `QUEST_` prefix for quest-local configuration only.
6. **Validation reports all errors** at once, not just the first. Error messages name the source (file, env var, flag).
7. **Tests never depend on the calling environment.** Pass config values explicitly; use `t.Setenv` only when testing `Load()` itself.

### CLI Surface Versioning

8. **Adding fields, flags, and commands is always safe.** Do it freely.
9. **Never remove, rename, or retype a JSON output field** without a deprecation cycle and a changelog entry. If a contract test fails, you are breaking agents.
10. **New request flags are always optional** with sensible defaults. Never add a required flag to an existing command.
11. **Exit codes 1-7 are permanent.** The mapping to OTEL error classes is a contract; changing it is a breaking change in both directions.
12. **Stderr messages are for humans.** Never write agent logic that parses them. Never assume agents parse them.
13. **Every schema change** ships a numbered forward-only migration and a bumped `schema_version`.
14. **Add a changelog entry** for every user-visible change: new commands, new flags, new fields, changed defaults, bug fixes, deprecations, schema bumps.
15. **When deprecating:** add a changelog entry, log a `WARN` on use, and plan removal for the next major version.

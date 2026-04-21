# Changelog

## [Unreleased]

### Added

- Enforce the 128-byte cap on task `title` at every write path per spec §Field constraints. `quest create --title` and `quest update --title` exit 2 with a usage error naming the flag and the observed byte count; `quest batch` reports over-limit titles as a per-line `field_too_long` JSONL error in the `semantic` phase, carrying `field`, `limit`, and `observed`. Bytes, not code points — consistent with the `@file` 1 MiB limit.
- `quest backup --to PATH` writes a transaction-consistent snapshot of `.quest/quest.db` via SQLite's online backup API, plus a sidecar copy of `.quest/config.toml` at `PATH.config.toml`. Safe to run while agents operate on the workspace. Elevated-role-gated. See spec §Backup & Recovery.
- Automatic pre-migration snapshot: when the binary runs pending schema migrations, it first writes `.quest/backups/pre-v{N}-{timestamp}.db` via SQLite's online backup API. Snapshot failure aborts the migration (exit 1). See spec §Storage > Pre-migration snapshot.

### Changed

- Terminal status value renamed from `complete` to `completed` to match the past-participle pattern of the other terminal statuses. The CLI command `quest complete`, the `tx_kind=complete` telemetry attribute, and log event `"quest command complete"` are unchanged. Existing `.quest/quest.db` files migrate automatically on next invocation via schema v2.
- Session-ownership enforcement on `quest update` / `complete` / `fail` is now controlled by `enforce_session_ownership` in `.quest/config.toml` and defaults to `false` (spec §Role Gating > Session ownership). Previously the check was unconditional after `quest accept`; on upgrade, projects that omit the field load with enforcement off and no longer need `AGENT_SESSION` coordination between cooperating callers. Operators who want the old strict behavior set `enforce_session_ownership = true`. `owner_session`, `handoff_session`, and history `session` are recorded identically in both modes, so telemetry and retrospectives are unaffected.
- Role gating is now opt-in restriction rather than opt-in elevation (spec §Role Gating > Resolution order). An unset `AGENT_ROLE` gets the full command surface; an explicit `AGENT_ROLE` in `elevated_roles` also gets the full surface; an explicit `AGENT_ROLE` *not* in `elevated_roles` is the only state that reduces the caller to worker commands and returns exit 6 on anything else. Previously an unset role defaulted to the worker surface, which forced humans and non-vigil callers to export `AGENT_ROLE=planner` to use quest at all, and invited dispatched agents to self-elevate. Vigil activates the gate by setting an explicit role on every dispatch; a dispatched worker cannot lift its own restriction because the explicit role is what enables the gate.
- Worker commands (`show`, `accept`, `update`, `complete`, `fail`) no longer fall back to `AGENT_TASK` when a task ID is omitted. The task ID is now a required positional argument; a missing ID returns exit 2 (`usage_error`). `AGENT_TASK` remains an identity/telemetry env var set by vigil (recorded as `dept.task.id` on spans and log records) but is not consumed as a CLI default. Rationale: letting identity env vars double as CLI conveniences invited agents to set their own `AGENT_TASK` / `AGENT_ROLE` / `AGENT_SESSION` when constructing commands, which undermines both role gating and session ownership as intended signals from the framework.

### Deprecated
### Removed
### Fixed
### Schema

- Schema v3: added `CHECK` constraints on `tasks.status`, `tasks.type`, `dependencies.link_type`, and `history.action`. Previously the valid enum set was enforced only by Go-level guards in command handlers, so a future code path that bypassed them -- or any direct `sqlite3` write -- could plant an invalid value (the `complete` vs `completed` drift fixed by schema v2 is the precedent). Migrations now run on a dedicated connection with `PRAGMA foreign_keys=OFF` so the documented CREATE/INSERT/DROP/RENAME recreation pattern can retire the old tables; `PRAGMA foreign_key_check` audits integrity inside the transaction before commit, preserving the forward-only-never-partial contract. Existing `.quest/quest.db` files migrate automatically on next invocation.

## [v0.1.0] - 2026-04-19

Initial release. Implements the v4 behavioral contract in [`docs/quest-spec.md`](docs/quest-spec.md).

### Added

- Workspace discovery by walking up from CWD to find `.quest/`, with `quest init --prefix` to create it.
- Worker commands: `quest show`, `accept`, `update`, `complete`, `fail`. `AGENT_TASK` defaults the target ID.
- Planner commands (gated by `AGENT_ROLE` against `elevated_roles`): `create`, `batch`, `cancel`, `reset`, `move`, `link`, `unlink`, `tag`, `untag`, `deps`, `list`, `graph`, `export`.
- System commands: `quest version`, `quest init`.
- Typed relationships — parent/child, `blocked-by`, `caused-by`, `discovered-from`, `retry-of` — with cycle and depth validation.
- `quest batch` accepts a JSON file of creates and edges; `--partial-ok` lands the lines that validated when others fail.
- Append-only history (`action`, `role`, `session`, `timestamp`, payload) written on every state-changing mutation; `quest show --history` surfaces it.
- `@file` and `@-` (stdin) input on every free-form flag (`--debrief`, `--description`, `--context`, `--note`, `--handoff`, `--reason`, `--acceptance-criteria`); 1 MiB cap; single-use `@-` per invocation.
- Structured JSON output on stdout per spec §Write-command output shapes; human-oriented `--format text` rendering.
- Stable exit codes (0/1/2/3/4/5/6/7) mapped from structured error sentinels; deterministic error precedence (role → existence → permission → state → usage).
- Serialized writes via `BEGIN IMMEDIATE` with a 5 s busy timeout; exit code 7 on contention (no internal retry — caller decides).
- Slog diagnostic logging on stderr with canonical event messages and attribute sets per `docs/OBSERVABILITY.md`.
- OpenTelemetry instrumentation (spans, metrics, slog bridge). Activates when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; zero-cost no-op when unset. `OTEL_GENAI_CAPTURE_CONTENT` opts into content attributes.
- `quest export` writes a snapshot of per-task JSON, markdown debriefs, and JSONL history to `<workspace>/quest-export/` (sibling of `.quest/`, or `--dir`).
- Test suite: stdlib-only table-driven tests, Layer 2 contract tests (output shapes, history, precondition events), Layer 3/4 integration tests, Layer 5 race/concurrency tests.
- GitHub Actions CI running `make ci` (race + integration tags + coverage) on every push.

### Schema

- Initial schema (`schema_version = 1`) with `tasks`, `history`, `dependencies`, `tags`, `notes`, `prs`, `meta` tables. Migrations are forward-only; a newer-than-supported schema version causes the binary to refuse to run (exit 1).

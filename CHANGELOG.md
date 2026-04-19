# Changelog

## [Unreleased]

### Added
### Changed
### Deprecated
### Removed
### Fixed
### Schema

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
- `quest export` writes a snapshot of per-task JSON, markdown debriefs, and JSONL history to `.quest/export/` (or `--dir`).
- Test suite: stdlib-only table-driven tests, Layer 2 contract tests (output shapes, history, precondition events), Layer 3/4 integration tests, Layer 5 race/concurrency tests.
- GitHub Actions CI running `make ci` (race + integration tags + coverage) on every push.

### Schema

- Initial schema (`schema_version = 1`) with `tasks`, `history`, `links`, `tags`, `notes`, `prs`, `meta` tables. Migrations are forward-only; a newer-than-supported schema version causes the binary to refuse to run (exit 1).

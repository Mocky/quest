# Agent Guide for quest

This file is for AI agents working on the quest codebase. Read this before making changes.

## What quest is

quest is a task tracker for AI agent workflows. It is one of four tools in the grove agent orchestration framework (alongside `lore`, `rite`, and `vigil`). Planning agents use quest to decompose deliverables into task graphs; worker agents use quest to read their assigned task, record progress, and file debriefs.

quest has an **agent-first design** — its command surface, output format, and data model are optimized for programmatic use by LLM-based agents. Human usability is a secondary concern.

quest is **reasoning-free**. It stores, indexes, and reports on tasks. All judgment about decomposition, scheduling, and task relationships lives in the agents that call it.

## Status

quest v0.1.0 shipped on 2026-04-19 (see `CHANGELOG.md`). The v4 behavioral contract in `docs/quest-spec.md` is the source of truth; ongoing work is incremental changes against it.

## Framework context

Before making non-trivial design decisions, read:

- `~/dev/grove/GROVE.md` — framework manifest, the four tools, dependency direction, settled design principles
- `docs/quest-spec.md` — quest's canonical behavioral contract (v4, 2026-04-16)
- `docs/impact-filter-quest.md` — success criteria and the reasoning behind quest's existence

Grove's settled principles that directly constrain quest:

- **Agent-at-the-top.** Framework code is dumb; decisions live in LLMs. Quest does not retry, does not infer intent, does not schedule.
- **Tools do not know about each other.** Quest stores lore memory IDs as opaque strings. Rite stores quest task IDs as opaque strings. No cross-tool validation.
- **Every agent session has exactly one task.** Workers see only the commands needed to execute their one task. Query/structural commands are elevated-role-only.
- **Substrate is disposable.** Task data must be exportable to human-readable files (`quest export`) so nothing is lost when a binary or database is replaced.

## Folder structure

```
docs/                          Specs and standards (see below)
```

Code layout (`cmd/`, `internal/`, etc.) will be added as we build. Follow the lore layout as a reference model — this file will be expanded with a detailed folder map once the skeleton exists.

## Documentation

All of these are binding. If you are a coding agent, treat every MUST/MUST NOT in them as a hard constraint.

- `docs/quest-spec.md` — behavioral contract. The source of truth for commands, flags, schema, status transitions, and exit codes. If code and spec disagree, the spec wins and the code is wrong.
- `docs/STANDARDS.md` — configuration management and CLI/API versioning conventions. All config flows through `internal/config/`; no other package reads env vars, flags, or `.quest/config.toml` directly.
- `docs/OBSERVABILITY.md` — logging and error-reporting rules. Read before adding log statements, changing error responses, or touching the wire protocol (when we add one).
- `docs/OTEL.md` — OpenTelemetry design. All OTEL API usage will live in one `internal/telemetry/` package; no other package imports OTEL directly. Telemetry activates when `OTEL_EXPORTER_OTLP_ENDPOINT` is set and is zero-cost when disabled.
- `docs/TESTING.md` — test strategy. Stdlib only, table-driven, no testify.

When the spec and a standards doc disagree, the spec wins — update the standards doc.

## Key design decisions to preserve

These are the load-bearing decisions from the spec. Read the spec section in parentheses for the full reasoning before changing any of them.

- **SQLite with WAL mode, single project per workspace** (Storage). Walk up from CWD to find `.quest/`. One project per workspace — nested quest projects are not supported.
- **Serialized writes, 5s busy timeout, exit code 7 on contention.** Quest does not retry internally. The caller (the agent) decides whether to retry. A rising rate of exit-7 returns is the signal to upgrade to the deferred `questd` daemon.
- **Structural transactions use `BEGIN IMMEDIATE`.** Any command that does a multi-row check (parent acceptance, cascade cancel, move, complete-with-children) must hold the write lock from the start of the transaction. Simple single-row transitions can use atomic `UPDATE ... WHERE` with `RowsAffected` checks.
- **Role gating is opt-in restriction, not opt-in elevation.** An unset `AGENT_ROLE` gets the full command surface (humans at a shell, or any caller outside vigil). An explicit `AGENT_ROLE` whose value is in `elevated_roles` gets the full surface. An explicit `AGENT_ROLE` whose value is *not* in `elevated_roles` is gated down to worker commands (`show`, `accept`, `update`, `complete`, `fail`) and returns exit 6 on anything else. Vigil activates the gate by setting an explicit role on every dispatch.
- **`AGENT_TASK` is identity/telemetry metadata, not a CLI default.** Vigil stamps it on dispatch so quest can record `dept.task.id` on spans and correlate logs. Worker commands **always** take the task ID as a positional argument -- they do not fall back to `AGENT_TASK`. Identity env vars must not double as CLI convenience defaults; that was what invited agents to self-identify.
- **Append-only history.** Every mutation writes a history row keyed by task ID and timestamp. History is a separate table, not a JSON blob on the task row, so `quest show` without `--history` never pays for it.
- **Export is the archival format; the DB is the operational format.** `quest export` must produce human-readable files (per-task JSON, markdown debriefs, JSONL event streams) that can be inspected, backed up, and version-controlled without quest tooling. This is a hard success criterion.
- **Backup and export are distinct.** `quest export` materializes the DB into human-readable files for archive and review; it is not a restore source. `quest backup` produces a transaction-consistent DB snapshot for operational recovery. There is deliberately no `quest import` or `quest restore` — restore is a file swap (see spec §Backup & Recovery), and adding commands to wrap `mv` would push scheduling/retention reasoning into quest. Cadence, off-box storage, and verification are operator concerns.
- **Schema versioning is forward-only.** Migrations ship with the binary and run inside a single transaction. A newer-than-supported schema version causes the binary to refuse to run, never to silently corrupt data. Downgrades are restore-from-backup (the pre-migration snapshot, or a prior `quest backup`), not schema rollback. Export is an archive, not a restore source.
- **Structured errors with stable codes.** Exit codes and error codes are part of the API contract. Agents switch on them.
- **Typed relationships carry retrospective signal.** `caused-by` and `discovered-from` are not cosmetic — they are the inputs the retrospective queries run against. Don't collapse them into a generic "related" link.
- **`quest help <cmd>` is the only help form** (grove decision 2026-05-06). Flag-form help (`--help`, `-h` in any position) is rejected at `cli.Execute` Step 0 with a two-line "did you mean: quest help <cmd>" redirect that mimics the typo-suggestion shape. Coverage is a contract: every row in `internal/cli/dispatch.go` `descriptors` MUST expose a non-nil `HelpFlagSet` (the `help` row itself is the documented exception). Tests derive the roster from `descriptors`, never from a hand-maintained list. See `docs/STANDARDS.md` §Help Convention.

## What not to do

- Don't add LLM calls to quest. All reasoning belongs in callers.
- Don't make quest aware of lore, rite, or vigil internals. Memory IDs, workflow IDs, and session IDs are opaque strings.
- Don't add retry loops around the write lock. Return exit code 7 and let the caller decide.
- Don't weaken the role gate when `AGENT_ROLE` is an explicit non-elevated value. Role gating is a security and context-window concern for dispatched workers -- it is not a convenience feature, and it must not be bypassed by the caller self-identifying.
- Don't bypass `internal/config/`. No `os.Getenv`, no `flag.Parse`, no direct TOML reads anywhere else.
- Don't import OTEL outside `internal/telemetry/` (once that package exists).
- Don't change error codes or exit codes without updating the spec. Agents depend on them.
- Don't write the implementation ahead of the spec. If the spec is silent or ambiguous on a question you need answered, stop and resolve it in the spec first.

# quest

A task tracker for AI agent workflows. Built for the [Grove](https://github.com/Mocky/grove) agent orchestration framework.

quest stores, indexes, and reports on tasks. Planning agents decompose deliverables into task graphs; worker agents read their assigned task, record progress, and file debriefs. All judgment about decomposition, scheduling, and retries lives in the agents — quest is **reasoning-free**.

## What quest does

- **Stores tasks** as rows in a local SQLite database (`.quest/quest.db`) with typed relationships (`blocked-by`, `caused-by`, `discovered-from`, `retry-of`, parent/child).
- **Enforces status transitions** — every mutation runs in a `BEGIN IMMEDIATE` transaction; write contention returns exit 7 for the caller to retry.
- **Records history** — every state change writes one append-only history row, enabling audit and retrospective queries.
- **Role-gates commands** — workers see only `show`, `accept`, `update`, `complete`, `fail`; planners see the full surface. Gating is driven by `AGENT_ROLE` against `elevated_roles` in `.quest/config.toml`.
- **Defaults from environment** — `AGENT_TASK` becomes the target for worker commands, eliminating repetitive arguments in agent tool calls.
- **Accepts `@file` input** — free-form flags (`--debrief`, `--description`, `--context`, `--note`, `--handoff`, `--reason`, `--acceptance-criteria`) accept `@path/to/file` or `@-` (stdin) so agents can pipe large bodies without shell escaping.
- **Exports to human-readable files** — `quest export` writes per-task JSON, markdown debriefs, and JSONL event streams that survive a binary replacement.
- **Emits OpenTelemetry** — when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; zero-cost when disabled.

## What quest does not do

- **No retries.** Write contention returns exit 7; the caller decides whether to retry.
- **No scheduling.** Quest does not assign tasks to agents. That is the framework's job (vigil in Grove).
- **No LLM calls.** quest is a tool-tier service; all reasoning belongs to the agents that call it.
- **No cross-tool knowledge.** quest stores lore memory IDs and rite workflow IDs as opaque strings. No cross-tool validation.
- **No nested workspaces.** Walk-up discovery stops at the first `.quest/` marker; one project per workspace.

## Installation

```bash
git clone https://github.com/Mocky/quest.git
cd quest
make build
```

This produces a `quest` binary at the repository root. Put it on `$PATH` or invoke it directly.

Requirements: Go 1.25+. The SQLite driver is `modernc.org/sqlite` (pure Go); no C toolchain is needed.

## Quick start

### 1. Initialize a workspace

```bash
cd ~/project
quest init --prefix proj
```

This creates `.quest/quest.db` and `.quest/config.toml`. The prefix (1-8 lowercase alphanumeric characters) appears in every task ID and is immutable for the project's lifetime.

### 2. Create tasks (planner role)

```bash
export AGENT_ROLE=planner

quest create --title "Ship v0.1" --description "Cut the first release"
# → {"id":"proj-a1","status":"open"}

quest create --title "Write changelog" --parent proj-a1 --tier T2
# → {"id":"proj-a1.1","status":"open"}

quest create --title "Tag and push" --parent proj-a1 --blocked-by proj-a1.1 --tier T1
# → {"id":"proj-a1.2","status":"open"}
```

### 3. Execute a task (worker role)

```bash
export AGENT_ROLE=   # unset (worker default)
export AGENT_TASK=proj-a1.1
export AGENT_SESSION=sess-042

quest show                  # read the task
quest accept                # transition open → accepted
quest update --note "Drafted the v0.1 entry"
quest complete --debrief "Release notes written; see CHANGELOG.md"
```

### 4. Inspect the graph (planner)

```bash
AGENT_ROLE=planner quest graph proj-a1          # tree rooted at the epic
AGENT_ROLE=planner quest deps  proj-a1.2        # direct blockers
AGENT_ROLE=planner quest list  --status open    # all open tasks
```

### 5. Archive

```bash
AGENT_ROLE=planner quest export              # writes .quest/export/
AGENT_ROLE=planner quest export --dir out    # writes out/
```

## Command reference

Worker commands (no role required):

| Command    | Purpose                                                |
| ---------- | ------------------------------------------------------ |
| `show`     | Read a task (defaults to `AGENT_TASK`); `--history`    |
| `accept`   | Transition `open → accepted` (race-safe, first wins)   |
| `update`   | Append notes / PRs / handoff; elevated flags gated     |
| `complete` | Mark as `complete` with `--debrief` (required)         |
| `fail`     | Mark as `failed` with `--debrief` (required)           |

Planner commands (require an elevated `AGENT_ROLE`):

| Command   | Purpose                                                     |
| --------- | ----------------------------------------------------------- |
| `create`  | Create a task; `--title` required, most other fields optional |
| `batch`   | Apply a JSON file of creates + edges; `--partial-ok`        |
| `cancel`  | Cancel a task (or subtree with `-r`); `--reason`            |
| `reset`   | Clear ownership + move back to `open`; `--reason`           |
| `move`    | Reparent a task (`--parent`); open-ancestors only           |
| `link`    | Add a typed edge (`--blocked-by`/`-caused-by`/…)            |
| `unlink`  | Remove a typed edge; idempotent                             |
| `tag`     | Add comma-separated tags to a task                          |
| `untag`   | Remove tags; idempotent                                     |
| `deps`    | List direct dependencies and their statuses                 |
| `list`    | Filter tasks (`--status`, `--role`, `--tier`, `--tag`, …)   |
| `graph`   | Render the tree rooted at an ID                             |
| `export`  | Write the archive (`.quest/export/` or `--dir`)             |

System commands (no role required):

| Command   | Purpose                       |
| --------- | ----------------------------- |
| `init`    | Create `.quest/` in the CWD   |
| `version` | Print `{"version":"…"}`       |

Per-command flags are discoverable via `quest <command> --help`.

The full behavioral contract — input conventions, JSON output shapes, error precedence, idempotency rules, and schema — lives in [`docs/quest-spec.md`](docs/quest-spec.md).

## Role gating

```toml
# .quest/config.toml
elevated_roles = ["planner"]
id_prefix      = "proj"
```

Resolution:

1. `AGENT_ROLE` is read from the environment.
2. If unset or not in `elevated_roles`, the role is treated as a **worker**.
3. Planner commands invoked by a worker return exit 6 (`role_denied`) without touching the database — role denial is uniform regardless of whether the referenced task exists.

Humans running quest from a shell default to the worker surface. To access planner commands, set `AGENT_ROLE` inline or export it:

```bash
AGENT_ROLE=planner quest list
export AGENT_ROLE=planner
```

## Exit codes

Exit codes are part of the contract. Agents switch on them.

| Code | Class              | Meaning                                               |
| ---- | ------------------ | ----------------------------------------------------- |
| 0    | Success            | Command succeeded                                     |
| 1    | `internal`         | General failure (panic, I/O, schema too new)          |
| 2    | `usage_error`      | Bad arguments, missing flag, unknown command          |
| 3    | `not_found`        | Task ID does not exist                                |
| 4    | `permission_denied`| Ownership check failed (non-owning worker)            |
| 5    | `conflict`         | Task state prevents operation (terminal, cycle, …)    |
| 6    | `role_denied`      | Elevated command attempted by a non-elevated role     |
| 7    | `locked`           | Write lock contention — safe to retry                 |

See `docs/quest-spec.md` §Error precedence for which class wins when multiple checks would fail.

## Configuration

### Environment variables

| Variable                       | Default | Purpose                                                                  |
| ------------------------------ | ------- | ------------------------------------------------------------------------ |
| `AGENT_ROLE`                   | unset   | Agent role (compared against `elevated_roles` for gating)                |
| `AGENT_TASK`                   | unset   | Default task ID for worker commands                                      |
| `AGENT_SESSION`                | unset   | Opaque session ID, stamped on history rows and `owner_session`           |
| `QUEST_LOG_LEVEL`              | `warn`  | slog verbosity: `debug`, `info`, `warn`, `error`                         |
| `QUEST_LOG_OTEL_LEVEL`         | `info`  | slog record level at which to ship to the OTEL log exporter              |
| `OTEL_EXPORTER_OTLP_ENDPOINT`  | unset   | When set, telemetry activates; empty / unset means zero-cost no-ops      |
| `OTEL_GENAI_CAPTURE_CONTENT`   | `false` | Opt-in content capture on spans (see `docs/OTEL.md`)                     |
| `TRACEPARENT` / `TRACESTATE`   | unset   | W3C trace context inbound from the caller                                |

All configuration flows through `internal/config/`; no other package reads environment variables, flags, or `.quest/config.toml` directly.

### Global flags

| Flag              | Purpose                                          |
| ----------------- | ------------------------------------------------ |
| `--format json`   | Default. Machine-readable output on stdout.      |
| `--format text`   | Human-readable rendering (not a contract).       |
| `--log-level`     | Override `QUEST_LOG_LEVEL` for one invocation.   |

## How quest fits in Grove

quest is one of four tools in the Grove framework:

| Tool    | Purpose                                              |
| ------- | ---------------------------------------------------- |
| `quest` | Tasks — planning, execution, retrospective           |
| `lore`  | Memory — durable, searchable agent knowledge         |
| `rite`  | Workflows — deterministic agent orchestration        |
| `vigil` | Observability — session logs, assignment, rotation   |

Tools do not know about each other's internals. quest stores lore memory IDs and rite workflow IDs as opaque strings; rite and vigil store quest task IDs the same way. Cross-tool queries are agent-level operations, not framework primitives.

## Documentation

- [`docs/quest-spec.md`](docs/quest-spec.md) — behavioral contract (commands, flags, JSON shapes, error codes, schema). The source of truth.
- [`docs/STANDARDS.md`](docs/STANDARDS.md) — configuration management and CLI/API versioning conventions.
- [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md) — logging and error-reporting rules.
- [`docs/OTEL.md`](docs/OTEL.md) — OpenTelemetry design.
- [`docs/TESTING.md`](docs/TESTING.md) — test strategy.
- [`AGENTS.md`](AGENTS.md) — orientation for coding agents working on the quest codebase itself.

## License

MIT

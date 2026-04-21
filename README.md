# quest

A task tracker for AI agent workflows. Built for the [Grove](https://github.com/Mocky/grove) agent orchestration framework.

quest stores, indexes, and reports on tasks. Planning agents decompose deliverables into task graphs; worker agents read their assigned task, record progress, and file debriefs. All judgment about decomposition, scheduling, and retries lives in the agents — quest is **reasoning-free**.

## What quest does

- **Stores tasks** as rows in a local SQLite database (`.quest/quest.db`) with typed relationships (`blocked-by`, `caused-by`, `discovered-from`, `retry-of`, parent/child).
- **Enforces status transitions** — every mutation runs in a `BEGIN IMMEDIATE` transaction; write contention returns exit 7 for the caller to retry.
- **Records history** — every state change writes one append-only history row, enabling audit and retrospective queries.
- **Role-gates commands** — workers see only `show`, `accept`, `update`, `complete`, `fail`; planners see the full surface. Gating is driven by `AGENT_ROLE` against `elevated_roles` in `.quest/config.toml`.
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

### 2. Create tasks

```bash
quest create --title "Ship v0.1" --description "Cut the first release"
# → {"id":"proj-a1","status":"open"}

quest create --title "Write changelog" --parent proj-a1 --tier T2
# → {"id":"proj-a1.1","status":"open"}

quest create --title "Tag and push" --parent proj-a1 --blocked-by proj-a1.1 --tier T1
# → {"id":"proj-a1.2","status":"open"}
```

`quest create` is a planner command. It works here because `AGENT_ROLE` is unset — role gating is **opt-in restriction**, so callers outside vigil (humans at a shell, ad-hoc scripts) get the full command surface. See [Role gating](#role-gating).

### 3. Execute a task

```bash
quest show     proj-a1.1
quest accept   proj-a1.1
quest update   proj-a1.1 --note "Drafted the v0.1 entry"
quest complete proj-a1.1 --debrief "Release notes written; see CHANGELOG.md"
```

Worker commands always take the task ID as a positional argument. When vigil dispatches an agent, it passes the task ID in the agent's prompt and *also* sets `AGENT_TASK` in the environment so quest can stamp it on telemetry as `dept.task.id` — but the env var is **not** a CLI default, and no agent should be constructing commands that depend on it being set.

### 4. Inspect the graph

```bash
quest graph proj-a1          # tree rooted at the epic
quest deps  proj-a1.2        # direct blockers
quest list  --status open    # all open tasks
```

These are planner commands; they work here for the same reason as step 2 — no `AGENT_ROLE` is set, so the gate is off.

### 5. Back up and archive

```bash
# Operational backup — restorable DB snapshot (safe while agents are running)
quest backup --to /backups/quest-$(date +%F).db

# Human-readable archive — review, audit, long-term storage
quest export                 # writes <workspace>/quest-export/
quest export --dir out       # writes out/
```

`quest backup` produces a restorable database snapshot; `quest export` produces the human-readable archive. They serve different purposes — see [`docs/quest-spec.md`](docs/quest-spec.md) §Backup & Recovery for when to use which, plus the restore procedure.

## Command reference

Worker commands (no role required):

| Command    | Purpose                                                |
| ---------- | ------------------------------------------------------ |
| `show`     | Read a task; `--history` includes the mutation log    |
| `accept`   | Transition `open → accepted` (race-safe, first wins)   |
| `update`   | Append notes / PRs / commits / handoff; elevated flags gated |
| `complete` | Mark as `completed` with `--debrief` (required)        |
| `fail`     | Mark as `failed` with `--debrief` (required)           |

Planner commands (gated — available when `AGENT_ROLE` is unset or in `elevated_roles`):

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
| `backup`  | Write a restorable DB snapshot (`--to PATH`)                |
| `export`  | Write the human-readable archive (`<workspace>/quest-export/` or `--dir`) |

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

Role gating is **opt-in restriction**. Resolution:

1. `AGENT_ROLE` **unset** → full command surface (workers + planners). This is the default for humans running quest from a shell and for any caller outside the Grove framework. No env-var setup required.
2. `AGENT_ROLE` **set** to a value in `elevated_roles` → full command surface.
3. `AGENT_ROLE` **set** to a value *not* in `elevated_roles` → worker surface only. Planner commands return exit 6 (`role_denied`) without touching the database — role denial is uniform regardless of whether the referenced task exists.

Vigil activates the gate by setting an explicit `AGENT_ROLE` on every dispatch (`worker`, `planner`, `coder`, etc.). A dispatched worker cannot self-elevate by overriding `AGENT_ROLE`, because the explicit value is what enables the gate. Outside vigil, quest trusts the caller — there is no need to set env vars just to use the tool.

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
| `AGENT_ROLE`                   | unset   | Agent role; activates role gating when set (see [Role gating](#role-gating)) |
| `AGENT_TASK`                   | unset   | Session's assigned task ID, stamped on telemetry as `dept.task.id`       |
| `AGENT_SESSION`                | unset   | Opaque session ID, stamped on history rows and `owner_session`           |
| `QUEST_LOG_LEVEL`              | `warn`  | slog verbosity: `debug`, `info`, `warn`, `error`                         |
| `QUEST_LOG_OTEL_LEVEL`         | `info`  | slog record level at which to ship to the OTEL log exporter              |
| `OTEL_EXPORTER_OTLP_ENDPOINT`  | unset   | When set, telemetry activates; empty / unset means zero-cost no-ops      |
| `OTEL_GENAI_CAPTURE_CONTENT`   | `false` | Opt-in content capture on spans (see `docs/OTEL.md`)                     |
| `TRACEPARENT` / `TRACESTATE`   | unset   | W3C trace context inbound from the caller                                |

All configuration flows through `internal/config/`; no other package reads environment variables, flags, or `.quest/config.toml` directly.

### Global flags

| Flag              | Purpose                                                              |
| ----------------- | -------------------------------------------------------------------- |
| `--text`          | Render human-readable output (not a contract). JSON is the default.  |
| `--log-level`     | Override `QUEST_LOG_LEVEL` for one invocation.                       |

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

# Quest CLI -- Specification

## Overview

Quest is a task tracking CLI for AI agent workflows. It is a core component of an agent orchestration framework where planning agents decompose deliverables into task graphs and worker agents execute individual tasks.

Quest has an **agent-first design**: its command surface, output format, and data model are optimized for programmatic use by LLM-based agents, with human usability as a secondary concern. Key design decisions follow from this:

- Worker agents see only the commands they need, minimizing context window usage
- Output format is controlled by `--format json|text` (default: `json`)
- Env var defaults eliminate repetitive arguments in agent tool calls
- The task schema carries exactly the information an agent needs to do its work

---

## Workflow Phases

Quest supports four phases of a delivery lifecycle. Understanding these phases is essential context for the command and schema design.

### 1. Planning

A planning agent receives an implementation plan and decomposes it into a task graph. This involves:

- Creating tasks with descriptions, context, dependencies, and tier assignments
- Linking tasks via parent-child relationships (epics -> tasks -> sub-tasks)
- Linking tasks via dependency relationships (task B is blocked by task A)
- Assigning model tiers so each task is handled by the cheapest tier that can get the job done
- Verifying the resulting dependency graph is correct

The planning agent typically creates all tasks for a deliverable in a single session using `quest batch`, then verifies the graph with `quest graph`.

### 2. Execution

Worker agents are started by the framework to execute individual tasks. A worker:

- Reads its assigned task with `quest show` (task ID comes from the `AGENT_TASK` env var)
- Checks dependency status with `quest deps` to understand what prior work was completed
- Signals it has begun with `quest accept`
- Records progress with `quest update --note`
- Completes with `quest complete --debrief` or reports failure with `quest fail --reason`

Workers only see worker-level commands. They cannot create, cancel, or modify task structure.

### 3. Review & Testing

After execution, the deliverable undergoes review and testing. Issues discovered during this phase are tracked as bug-type tasks linked back to the originating work:

- `caused-by` links trace a bug to the task whose work introduced it
- `discovered-from` links trace a bug to the task whose testing revealed it

These typed relationships enable pattern detection in the retrospective phase.

### 4. Retrospective

After delivery, a retrospective reviews the full task graph to identify improvement opportunities:

- Which tasks failed and why?
- Which agents/tiers produced bugs?
- Are there recurring patterns in caused-by links?
- Can future prompts be improved to minimize issues?

Quest provides the data for this analysis. The retrospective queries themselves are a framework concern, not quest commands.

---

## Tool Identity

Quest is a single-binary CLI tool.

- **Binary name:** `quest`
- **Workspace concept:** quest operates at the project level
- **Config location:** `.quest/config.toml`, discovered by walking up the directory tree from CWD
- **Data location:** `.quest/` directory in the project root
- **Concurrency:** serialized writes via file locking

---

## Role Gating

Quest exposes a minimal CLI surface for worker agents by default and the full command surface for elevated roles (planning agents). This minimizes the commands visible to workers, reducing context window usage when agents encounter errors or request help text.

### Resolution logic

Quest reads role and task context from environment variables:

```
AGENT_ROLE   -- the agent's role (e.g., "coder", "planner")
AGENT_TASK   -- the agent's assigned task ID (used as default for commands)
TRACEPARENT  -- OpenTelemetry trace context for observability
```

The task ID from `AGENT_TASK` is used as the default target for commands, so `quest accept tsk-123` becomes just `quest accept` when the env var is set.

### Resolution order

1. Read the agent's role from `AGENT_ROLE`
2. Read `elevated_roles` from `.quest/config.toml` (default: empty list)
3. If the command is worker-level: run it
4. If the command is elevated-level: check if the agent's role is in `elevated_roles`
5. If role is unset or not elevated: default to worker surface
6. If a worker attempts an elevated command: reject with a clear message and exit code 6

### Config file

```toml
# .quest/config.toml

# Role gating
elevated_roles = ["planner"]

# Task IDs
id_prefix = "proj"              # short prefix for this project, required at init
```

---

## Task IDs

Task IDs are generated with a project-specific prefix and monotonic base36 short IDs. When tasks are linked via parent-child relationships, the child inherits the parent's ID as its prefix with an appended `.N`, where N is a monotonically increasing base10 number. Sub-tasks can have their own sub-tasks.

### Format

```
{prefix}-{shortID}              # task ID
{prefix}-{shortID}.{N}          # sub-task ID
{prefix}-{shortID}.{N}.{N}      # sub-sub-task ID
```

### Examples

```
proj-42
proj-42.1
proj-42.1.1
```

---

## Output & Error Conventions

- Output format is controlled by `--format json|text` (default: `json`)
- `json` -- structured JSON to stdout, suitable for agent consumption
- `text` -- human-readable formatted output to stdout
- Warnings and errors always go to stderr regardless of format
- Flat JSON structures preferred over deeply nested
- Consistent types across all commands (durations in seconds, timestamps in ISO 8601)
- JSON Lines for streaming output (in json mode)

### Exit codes

| Code | Meaning                                                       |
| ---- | ------------------------------------------------------------- |
| 0    | Success                                                       |
| 1    | General failure                                               |
| 2    | Usage error (bad arguments)                                   |
| 3    | Resource not found                                            |
| 4    | Permission denied                                             |
| 5    | Conflict (resource already exists)                            |
| 6    | Role denied (elevated command attempted by non-elevated role) |

---

## Task Entity Schema

### Core fields

| Field         | Set by  | Description                                                     |
| ------------- | ------- | --------------------------------------------------------------- |
| `id`          | system  | Generated task ID (see Task IDs section)                        |
| `title`       | planner | Short description of the task                                   |
| `description` | planner | Full description -- the decomposed unit of work                  |
| `context`     | planner | Background information for the worker, injected into the prompt |
| `type`        | planner | `task` (default) or `bug`                                       |
| `status`      | system  | Current state (see lifecycle below)                             |
| `role`        | planner | Which role is assigned to execute the task                      |
| `tier`        | planner | Model tier assignment (see tier list below)                     |
| `labels`      | planner | Free-form tags (e.g., `go`, `sql`, `auth`, `concurrency`)       |
| `metadata`    | planner | Arbitrary JSON for planner-defined extensions and optimizations |

### Relationship fields

| Field          | Set by  | Description                                       |
| -------------- | ------- | ------------------------------------------------- |
| `parent`       | planner | Link to parent task/epic                          |
| `dependencies` | planner | Typed dependency list (see relationships section) |

### Execution fields

| Field     | Set by | Description                                                                 |
| --------- | ------ | --------------------------------------------------------------------------- |
| `pr`      | worker | Link to the PR containing task output                                       |
| `notes`   | worker | Array of timestamped progress notes                                         |
| `handoff` | worker | Context bridge for session continuity -- what the next session needs to know |
| `debrief` | worker | After-action report, submitted at completion or failure                     |

### Model tiers

The planning agent assigns a tier to each task to control which model executes it. The framework uses this field to select the appropriate model when starting a worker agent.

| Tier | Label      | Use case                                                           |
| ---- | ---------- | ------------------------------------------------------------------ |
| T0   | Tool       | No LLM needed -- handled by a tool or script                        |
| T1   | Minimal    | Classification, extraction, simple reformatting                    |
| T2   | Capable    | Summarization, straightforward code, routine Q&A                   |
| T3   | Strong     | Complex reasoning, nuanced writing, multi-step code                |
| T4   | Reasoning  | Extended-thinking tasks -- math, formal logic, planning             |
| T5   | Reasoning+ | Max-compute reasoning -- research-grade problems, proofs, hard code |
| T6   | Human      | Human attention, the most expensive tier                           |

---

## Status Lifecycle

```
open -> accepted -> complete
                -> failed
       (at any point) -> cancelled (planner only)
```

- **open** -- task has been created, may or may not be assigned
- **accepted** -- worker has acknowledged and begun work
- **complete** -- worker finished the task and submitted a debrief
- **failed** -- worker could not complete the task
- **cancelled** -- planner aborted the task before completion

The `open -> accepted` transition is the key diagnostic signal. A task that stays `open` means the agent never started. A task that reaches `accepted` but never completes means the agent started but failed mid-work.

---

## Relationship Types

Dependencies support typed relationships to enable richer graph queries and retrospective analysis:

| Type              | Meaning                                                                      |
| ----------------- | ---------------------------------------------------------------------------- |
| `blocks`          | Standard dependency -- this task must complete before the dependent can start |
| `caused-by`       | Bug tracing -- this bug was caused by work done in the linked task            |
| `discovered-from` | Bug tracing -- this bug was discovered during testing of the linked task      |

`blocks` is the default type when adding a dependency.

Sibling relationships are not modeled explicitly. Tasks that share a parent are siblings. `quest children PARENT-ID` returns all siblings of any task under that parent.

---

## Worker Commands

These commands are available to all agents regardless of role.

---

```
quest show [ID]
```

Display full task details including description, context, status, dependencies and their outcomes, notes, and handoff from any prior session.

If `ID` is omitted, uses the value of `AGENT_TASK`.

---

```
quest accept [ID]
```

Signal that the agent has received the task and begun work. Transitions status from `open` to `accepted`.

---

```
quest update [ID] [flags]
```

Write progress information to the task.

| Flag              | Description                                                      |
| ----------------- | ---------------------------------------------------------------- |
| `--note "..."`    | Append a timestamped progress note                               |
| `--pr "URL"`      | Link the PR containing task output                               |
| `--handoff "..."` | Set handoff context for session continuity (overwrites previous) |

---

```
quest complete [ID] [--debrief "..."] [--pr "URL"]
```

Mark the task as complete.

| Flag              | Description                         |
| ----------------- | ----------------------------------- |
| `--debrief "..."` | Free-form after-action report       |
| `--pr "URL"`      | Attach a PR link at completion time |

---

```
quest fail [ID] --reason "..." [--debrief "..."]
```

Mark the task as failed with a reason.

| Flag              | Description                    |
| ----------------- | ------------------------------ |
| `--reason "..."`  | Why the task failed (required) |
| `--debrief "..."` | Free-form after-action report  |

---

```
quest deps [ID]
```

Read-only. List all dependencies for this task, their statuses, and their outcomes. Includes typed relationships.

---

```
quest children ID
```

Read-only. Show all child tasks under this task.

---

## Planner Commands (Elevated)

These commands require an elevated role as defined in `.quest/config.toml`.

---

### Task Creation

```
quest create --title "..." [flags]
```

Create a new task.

| Flag                  | Description                           |
| --------------------- | ------------------------------------- |
| `--title "..."`       | Short task title (required)           |
| `--description "..."` | Full description of the work          |
| `--context "..."`     | Background information for the worker |
| `--parent ID`         | Link to parent task/epic              |
| `--type TYPE`         | `task` (default) or `bug`             |
| `--tier TIER`         | Model tier: T0, T1, T2, T3, T4, T5    |
| `--role ROLE`         | Assigned role                         |
| `--label LABEL`       | Add a label (repeatable)              |
| `--meta KEY=VALUE`    | Set a metadata field (repeatable)     |

---

```
quest batch FILE
```

Create multiple tasks from a JSONL file. Each line is a JSON object matching the `quest create` fields, with additional support for expressing relationships between tasks in the same batch.

Used by the planning agent to create an entire task graph in a single tool call after decomposing a deliverable.

The batch file supports a `ref` field for internal cross-referencing. Tasks can reference other tasks in the same batch by `ref` in their `parent` and `dependencies` fields. Quest resolves these references to real task IDs during import.

### Batch file format

```jsonl
{"ref": "epic-1", "title": "Auth module", "type": "task", "tier": "T3"}
{"ref": "task-1", "title": "JWT validation", "parent": "epic-1", "tier": "T2", "role": "coder"}
{"ref": "task-2", "title": "Session store", "parent": "epic-1", "tier": "T2", "role": "coder"}
{"ref": "task-3", "title": "Auth middleware", "parent": "epic-1", "tier": "T3", "role": "coder", "dependencies": [{"ref": "task-1", "type": "blocks"}, {"ref": "task-2", "type": "blocks"}]}
```

---

### Task Management

```
quest cancel ID --reason "..."
```

Cancel a task. Transitions status to `cancelled`. Only available to elevated roles.

---

### Dependencies

```
quest dep add SOURCE TARGET [--type blocks|caused-by|discovered-from]
```

Add a typed dependency. Default type is `blocks`.

```
quest dep remove SOURCE TARGET
```

Remove a dependency.

---

### Metadata

```
quest meta set ID KEY VALUE
```

Set a metadata field on a task.

---

### Queries

```
quest list [flags]
```

List tasks with filtering.

| Flag              | Description      |
| ----------------- | ---------------- |
| `--status STATUS` | Filter by status |
| `--parent ID`     | Filter by parent |
| `--label LABEL`   | Filter by label  |
| `--role ROLE`     | Filter by role   |
| `--type TYPE`     | Filter by type   |
| `--tier TIER`     | Filter by tier   |

---

```
quest ready
```

List tasks whose dependencies are all satisfied and are ready to be assigned/started. This is the primary command the framework uses to determine what work can be dispatched next.

---

```
quest graph ID
```

Display the dependency graph rooted at a task. Shows parent-child structure, dependency edges with types, and task statuses.

**`--format json`** (default): structured adjacency list with nodes (id, title, status, tier) and edges (source, target, type).

**`--format text`**: ASCII tree representation for visual inspection.

Used by the planning agent to verify graph correctness after batch task creation, and by humans to understand the structure of a deliverable.

---

## System Commands

```
quest init --prefix PREFIX
```

Initialize a quest project in the current directory. Creates `.quest/` directory and `.quest/config.toml`. Requires `--prefix` to set the ID prefix for this project.

---

```
quest version
```

Print version information.

---

## Cross-Tool Linking

Quest stores references to external systems (e.g., memory store entries) as opaque strings in notes, debrief text, handoff text, or metadata. Quest does not resolve or validate these references. The agent is the integration layer between tools, not quest.

---

## Framework Integration Points

These are concerns the framework handles -- not quest commands, but places where the framework interacts with quest:

| Concern             | Framework responsibility                                                                                                                             |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| Agent startup       | Framework reads task tier and role from quest, selects the appropriate model, sets `AGENT_ROLE` and `AGENT_TASK` env vars, and starts the agent      |
| Prompt construction | Framework reads task description and context from `quest show` output and injects them into the agent's prompt template                              |
| Work dispatch       | Framework calls `quest ready` to find dispatchable tasks and starts agents for them                                                                  |
| Recurring tasks     | Framework scheduler calls `quest create` or `quest batch` on a cron. Quest has no scheduling concept                                                 |
| Project setup       | Framework generates `.quest/config.toml` during project initialization                                                                               |
| Role injection      | Framework sets `AGENT_ROLE` when launching agents                                                                                                    |
| Analytics           | Outcome correlation, rework tracking, lead time, and pattern detection are queries over quest data handled by framework tooling -- not quest commands |

---

## Deferred / Future Concerns

- **Daemon architecture** -- if concurrent access patterns outgrow file locking, introduce a `questd` daemon with a TCP/JSON wire protocol
- **`quest assign`** -- explicit assignment command, if role on create proves insufficient
- **`quest tag`** -- dedicated label management command, if `--label` on create and metadata prove insufficient
- **`quest close-all`** -- batch status transitions, if scripting `quest list | quest complete` becomes cumbersome
- **Snapshots / branching** -- tagging and branching the task store for rollback or experimentation
- **`quest diff` / `quest log`** -- history exploration commands for audit and debugging
- **Expanded type taxonomy** -- additional task types beyond `task` and `bug` if query patterns demand it
- **Analytics CLI** -- queries like "tasks at T2 tier fail 3x more on concurrency work" belong in a separate analytics tool that reads quest's data store
- **Debrief processing workflow** -- dedicated commands for marking debriefs as reviewed, if note-based tracking proves insufficient
- **Estimate field** -- first-class effort estimation if the framework develops scheduling/budgeting capabilities that consume it

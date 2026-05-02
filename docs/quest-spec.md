# Quest CLI -- Specification

**Version: v4** | 2026-04-16

## Overview

Quest is a task tracking CLI for AI agent workflows. It is a core component of an agent orchestration framework where planning agents decompose deliverables into task graphs and worker agents execute individual tasks.

Quest has an **agent-first design**: its command surface, output format, and data model are optimized for programmatic use by LLM-based agents, with human usability as a secondary concern. Key design decisions follow from this:

- Worker agents see only the commands they need, minimizing context window usage
- Output is JSON on stdout by default -- the agent contract -- and `--text` selects a human-readable rendering
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

- Reads its assigned task with `quest show <id>` -- vigil injects the task ID into the agent's prompt, and also sets `AGENT_TASK` for telemetry correlation
- Signals it has begun with `quest accept`
- Records progress with `quest update --note`
- Completes with `quest complete --debrief` or reports failure with `quest fail --debrief`

Workers only see worker-level commands. They cannot create, cancel, or modify task structure.

### 3. Review & Testing

After execution, the deliverable undergoes review and testing. Issues discovered during this phase are tracked as bug-tagged tasks linked back to the originating work:

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
- **Concurrency:** serialized writes (see Storage section)
- **Scope:** quest is scoped one-project-per-workspace by design. Walk-up discovery stops at the first `.quest/` marker, so nested quest projects inside another quest project's tree are not supported -- the inner project would be invisible and its commands would resolve to the outer workspace. This matches grove's department-level positioning: a department is a single project. If a monorepo needs per-subproject task tracking, use multiple departments or run quest from outside the enclosing workspace

---

## Storage

Quest uses a SQLite database at `.quest/quest.db` for runtime storage.

- **Engine:** SQLite (embedded, no external server dependency)
- **Location:** `.quest/quest.db`, inside the project marker directory
- **WAL mode:** the database runs in WAL (write-ahead logging) mode for concurrent reads with serialized writes
- **Busy timeout:** all database connections must set `PRAGMA busy_timeout = 5000` so that concurrent writers wait up to 5 seconds for the write lock before returning an error. Without this, SQLite returns `SQLITE_BUSY` immediately when the lock is held, causing spurious failures in agent swarms. If the wait exceeds five seconds, quest exits with code 7 (transient failure) and a stderr message: `"write lock unavailable after 5s -- transient failure, safe to retry"`. Quest does not retry internally -- the caller decides whether and when to retry, consistent with the principle that intelligence lives in the LLM, not in code
- **Atomicity:** every status transition -- `quest accept`, `quest complete`, `quest fail`, `quest reset`, `quest cancel` -- runs inside a `BEGIN IMMEDIATE` transaction with an explicit SELECT-then-UPDATE, even for leaves. A single atomic `UPDATE ... WHERE id=? AND status=?` with a `RowsAffected` check cannot distinguish exit 3 (`not_found`) from exit 5 (`conflict`) and therefore violates the error-precedence contract in §Error precedence. Append-only fields (`notes`, `prs`, `commits`) use INSERT semantics. `handoff` uses upsert (INSERT or REPLACE) semantics
- **Structural transactions:** every write command uses `BEGIN IMMEDIATE` to acquire the write lock at transaction start, so the existence/role/permission/state checks required by §Error precedence all run inside the same transaction as the mutation. Commands that additionally touch multiple rows for structural reasons -- `quest accept` on a parent (must verify all children are in a terminal state), `quest create --parent` (must verify parent is in `open` status and depth limit), `quest complete` on parent tasks (must verify all children are terminal), `quest move` (must check circular parentage, depth limits, and update multiple rows), and `quest cancel -r` (must recursively update descendants) -- include those checks inside the same transaction. `quest update` uses `BEGIN IMMEDIATE` on every invocation regardless of flags, including `--note` / `--pr` / `--commit` on an already-owned task: the existence check and terminal-state gating are unconditional preconditions, and using a single transaction path keeps `quest.store.tx{tx_kind=update}` observability uniform across worker-only and mixed-flag paths
- **Schema:** the task entity is normalized across multiple tables -- `tasks` holds the mutable task row, `history` holds append-only mutation entries keyed by task ID and indexed by timestamp, `dependencies` holds typed links between tasks, and `tags` holds the task-tag join. History is a separate table (not a JSON array on the task row) so that `quest show` without `--history` never reads history pages, and future range queries (tail, stream, paginated audit) can be served by an indexed scan rather than a full-row deserialization. Internal schema details beyond this separation are an implementation concern, not API surface
- **Schema versioning:** the database stores a `schema_version` integer in a `meta` table. On startup, quest reads the version and compares it to the range the binary supports. If the version is higher than supported, quest refuses to operate (exit code 1, stderr: `"database schema version N is newer than this binary supports -- upgrade quest"`) so a stale binary cannot silently corrupt a newer schema. If the version is lower, quest runs the pending forward-only migrations inside a single transaction before proceeding; a failed migration leaves the database at the prior version. Downgrades are not supported -- rolling back a binary upgrade is done by swapping `.quest/quest.db` back with a prior file-level backup (see §Backup & Recovery). `quest export` is not a valid restore source; there is no `quest import`. Every migration is preceded by an automatic pre-migration snapshot (see next bullet) so the prior-version database file is always available on the same host without operator action. The initial release ships at `schema_version = 1`; every subsequent schema change ships a numbered migration and bumps the version. Migration code is part of the binary, not loaded from disk
- **Pre-migration snapshot:** when the binary detects that `schema_version` is lower than it supports and is about to run migrations, it first writes a transaction-consistent snapshot of `.quest/quest.db` to `.quest/backups/pre-v{N}-{timestamp}.db` via SQLite's online backup API, where `{N}` is the target schema version and `{timestamp}` is `YYYYMMDDTHHMMSSZ` (UTC). The snapshot is taken before the migration transaction opens. If the copy fails, the migration does not run and the binary exits 1 with a stderr message identifying the failed backup path. When `schema_version == 0` (fresh init), the snapshot is skipped because the prior-version file has no recoverable content. The snapshot is retained indefinitely; quest does not auto-prune. This is the only automatic backup quest performs -- ongoing operational backups are the operator's responsibility (see §Backup & Recovery)
- **Direct access:** the database can be queried with standard SQLite tooling (`sqlite3`, any SQLite client library) for ad-hoc inspection or custom analytics
- **Human-readable access:** quest data is materialized to human-readable files (per-task JSON, markdown debriefs, JSONL event streams) via `quest export`. The export is the archival and review format; the database is the operational format
- **Daemon upgrade path:** if concurrent write contention from many agent processes exceeds what SQLite's WAL-mode single-writer lock can sustain -- indicated by a rising rate of exit code 7 (transient failure) returns under normal operation -- the deferred `questd` daemon owns the database connection and serializes access at the application level. The contention threshold depends on write duration and burst patterns, not a fixed agent count; empirical profiling under real agent swarms determines when the upgrade is warranted. WAL mode supports unlimited concurrent readers; the bottleneck is exclusively concurrent writes contending on the write lock

---

## Backup & Recovery

Quest's operational data lives in `.quest/`. Two files matter for recovery:

- `.quest/quest.db` -- the SQLite database. Loss or corruption is unrecoverable without a backup; `quest export` is **not** a valid restore source (there is no `quest import`).
- `.quest/config.toml` -- records the immutable `id_prefix`. Losing it and recreating the project with a different prefix permanently decouples the new database from any external references (PR descriptions, debriefs, session logs, rite workflows) that captured the old task IDs.

Backups MUST include both files.

### Threat scope

Quest's in-code backup support targets one failure class only: a binary upgrade that runs a schema migration, where a bug in the migration or a data issue discovered after the migration makes the prior-version file the fastest recovery path. For this case, quest ships a pre-migration snapshot (§Storage > Pre-migration snapshot).

All other failure modes -- accidental `rm -rf .quest/`, disk corruption, hardware loss, host compromise, misaimed scripts -- are operator responsibility. Quest does not include a scheduler, retention policy, off-box writer, or integrity verifier for backups. Those are reasoning concerns and belong outside the tool (see `AGENTS.md` > Agent-at-the-top).

### Recommended operator strategy

1. **Periodic hot backups via `quest backup`.** Schedule it (cron, systemd timer, CI job, framework-level scheduler) at whatever cadence your risk tolerance demands. The command is safe to run while agents operate on the workspace.
2. **Off-box storage.** Write the backup to a path on a different disk, host, or backup service (restic, borg, S3, etc.). A backup on the same disk as the original protects only against accidental deletion, not disk failure.
3. **Independent verification.** At least once after setup, and periodically thereafter, restore into a throwaway workspace and run `quest version` + `quest list` against it. A backup no one has ever restored is not a backup.

### Restore procedure

Quest does not ship a `quest restore` command; restore is a file swap. Operators:

1. Stop all callers that might write to the workspace (workers, planners, schedulers, `questd` if deployed). If any caller writes during the swap, the restored state will be inconsistent with downstream records.
2. Remove or move the current `.quest/quest.db`, `.quest/quest.db-wal`, and `.quest/quest.db-shm`. The WAL/SHM sidecars are regenerated on next open; leaving stale copies alongside a restored `.db` can cause SQLite to see a confused state.
3. Copy the backed-up database file to `.quest/quest.db`.
4. Copy the backed-up `config.toml` to `.quest/config.toml` only if the current one is missing or damaged. Overwriting a healthy `config.toml` is almost never what you want -- the `id_prefix` there is the one that matches IDs in external references.
5. Run `quest version` to confirm the installed binary opens the restored database without a schema-version error.
6. If the restored database was written by a newer binary than the one now installed, the binary exits 1 (per §Storage > Schema versioning). Reinstall a compatible binary or restore from an older snapshot.
7. Resume callers.

Agents and framework schedulers MUST NOT invoke this procedure; restore is an out-of-band operator action.

### `quest export` is not a restore source

`quest export` remains the archival and audit format -- a one-way materialization for review, long-term storage, version control, and post-hoc analysis. Run it in parallel with `quest backup` if you want both operational recovery and a human-readable durable archive. The two solve different problems: export for humans and auditors, backup for operational continuity.

---

## Role Gating

Quest has two command surfaces: a minimal one (`show`, `accept`, `update`, `complete`, `fail`) for worker agents executing a single assigned task, and the full surface including planning, query, and structural commands. Which surface a caller sees is controlled by `AGENT_ROLE` -- the framework (vigil) activates the worker surface by setting an explicit non-elevated role on dispatch; callers that do not set `AGENT_ROLE` (humans, ad-hoc scripts) or that set it to an elevated value see the full surface. The design principle behind the worker surface: every agent session has exactly one task, and that task contains everything the agent needs to do its work. Workers do not browse, search, or query other tasks -- if information is missing, the principled fix is to inject it into the task (via context, description, or handoff), not to give the worker more commands. If a dependency is blocking work, that is a scheduling failure by the lead, not something the worker can resolve. Query and structural commands are reserved for elevated dispatch because only planning agents need a view across tasks.

### Resolution logic

Quest reads agent identity from environment variables set by the framework (vigil) when it dispatches an agent session:

```
AGENT_ROLE      -- the agent's role; activates role gating when set (see below)
AGENT_SESSION   -- unique session ID assigned by vigil (opaque string)
AGENT_TASK      -- the session's assigned task ID
TRACEPARENT     -- OpenTelemetry trace context for observability
```

All four are identity/correlation signals -- `AGENT_ROLE` gates the command surface, `AGENT_SESSION` is stamped on history rows and `owner_session`, `AGENT_TASK` is stamped on telemetry as `dept.task.id`, `TRACEPARENT` links quest spans to the caller's trace. **None of them default into command arguments.** Worker commands that operate on a task always take the task ID as a positional argument. Identity and CLI convenience are kept separate so that command-line invocations are explicit and self-contained, and so that agents are not invited to set their own identity to make commands work.

### Resolution order

Role gating is **opt-in restriction**, not opt-in elevation. An empty `AGENT_ROLE` means "no role claim was made" -- typical of a human running quest from a shell, or any caller outside vigil -- and the caller gets the full command surface. Gating activates only when a role has been explicitly set and does not match an elevated role.

1. Read `AGENT_ROLE` from the environment
2. Read `elevated_roles` from `.quest/config.toml` (default: empty list; `quest init` writes `["planner"]`)
3. If the command is worker-level: run it
4. If `AGENT_ROLE` is unset (empty): run it (elevated commands included)
5. If `AGENT_ROLE` is set and its value is in `elevated_roles`: run it
6. If `AGENT_ROLE` is set but its value is not in `elevated_roles`: reject with exit code 6 (`role_denied`)

The framework turns on the gate by setting an explicit `AGENT_ROLE` on every dispatch. A missing role from vigil is a vigil bug -- quest does not defend against it; vigil's own dispatch tests catch it. This design removes the temptation for a dispatched worker to set `AGENT_ROLE=planner` itself to reach an elevated command: if vigil set the role, the gate is active and self-elevation fails; if vigil did not set the role (human shell / non-vigil use), the gate is off and self-elevation is unnecessary.

### Config file

```toml
# .quest/config.toml

# Role gating
elevated_roles = ["planner"]

# Session ownership enforcement. When true, only the session that called
# `quest accept` (or an elevated role) can write progress / debrief on the
# task; a non-owning, non-elevated caller receives exit 4. When false, the
# owner_session is still recorded for audit/telemetry but is not enforced.
enforce_session_ownership = false

# Task IDs
id_prefix = "proj"              # short prefix for this project, set at init (see Prefix validation)
```

### Session ownership

`quest accept` records `owner_session` from `AGENT_SESSION` so every task carries the identity of the session that is executing it. Whether that ownership is *enforced* on subsequent writes is controlled by `enforce_session_ownership` in `.quest/config.toml`.

- `enforce_session_ownership = false` (default). `owner_session` is recorded and surfaced in `quest show` and history, but quest does not reject writes from a different session. Any caller that passes the role gate and the existence check may call `quest update`, `quest complete`, or `quest fail`. Use this when the framework driving quest already guarantees single-worker dispatch, or when humans and agents are expected to co-operate on the same task without session-id coordination.
- `enforce_session_ownership = true`. After `quest accept`, only the owning session (where `AGENT_SESSION == owner_session`) or a caller with an elevated role may call `quest update`, `quest complete`, or `quest fail`. A non-owning, non-elevated caller receives exit code 4 (`permission_denied`). Use this when the framework dispatches multiple agents that share a workspace and you want quest to act as a second line of defense against cross-session writes.

The setting only changes the permission check. `owner_session`, `handoff_session`, and history `session` values are recorded identically in both modes so retrospectives, telemetry, and crash recovery do not depend on the mode.

---

## Task IDs

Task IDs are generated with a project-specific prefix and monotonic base36 short IDs. When tasks are linked via parent-child relationships, the child inherits the parent's ID as its prefix with an appended `.N`, where N is a monotonically increasing base10 number. Sub-tasks can have their own sub-tasks.

### Format

```
{prefix}-{shortID}              # task ID (base36, project-global counter)
{prefix}-{shortID}.{N}          # sub-task ID (base10, per-parent counter)
{prefix}-{shortID}.{N}.{N}      # sub-sub-task ID (base10, per-parent counter)
```

### Examples

```
proj-01
proj-01.1
proj-01.1.1
```

### Prefix validation

The `id_prefix` is set once at `quest init --prefix` and is immutable for the life of the project. Prefixes must match `^[a-z][a-z0-9]{1,7}$`:

- 2-8 characters, lowercase letters and digits only
- Must start with a letter
- No hyphens, dots, underscores, or other punctuation. Hyphen and dot are structural separators in task IDs (`{prefix}-{shortID}.{N}`) and admitting them would make IDs ambiguous to parse. Other punctuation is excluded to keep IDs portable across shells, URLs, and filenames
- Reserved values: `ref`. Task IDs with this prefix would collide visually with the batch-file `ref` field used for internal cross-references (see `quest batch`)

`quest init` rejects invalid prefixes with exit code 2 (usage error) and an error message citing which rule failed. The length cap exists because prefixes appear in every task ID, and task IDs appear in prompts, rite references, export filenames, and history entries -- a bloated prefix multiplies across every reference.

### ID generation rules

- The top-level counter is project-global, stored in `.quest/quest.db`, starts at `1`, and increments per top-level task created
- Short IDs are base36, zero-padded to a minimum width of 2 characters (giving 1,296 values before the width grows to 3)
- Sub-task numbering (`.N`) uses a separate per-parent base10 counter, starting at `1`
- ID generation must be wrapped in an exclusive transaction during `quest create` and `quest batch` to prevent concurrent agents from generating duplicate short IDs
- Maximum nesting depth is 3 levels (e.g., `proj-a1.1.1`). `quest create --parent`, `quest batch`, and `quest move` fail with exit code 5 if the operation would produce a task deeper than 3 levels. The error message directs the planner to create a top-level task with a `blocked-by` link instead of nesting deeper. Three levels maps to the natural decomposition pattern of epic > task > sub-task; deeper nesting produces increasingly unwieldy IDs that consume agent context window tokens across every reference and typically indicates work that should be a separate task graph connected by dependency links

### Structural immutability

Parent-child structure is immutable once any task in the sub-graph has been dispatched. IDs encode parentage by construction -- `proj-01.1.3` is always a child of `proj-01.1` -- and this encoding must remain truthful once workers, external tooling, and retrospective references depend on it.

During the planning-and-verification window (after create, before any task in the sub-graph has been accepted), elevated roles can correct structural errors with `quest move ID --parent NEW_PARENT`. The task and all descendants receive new IDs reflecting the new parent namespace, all dependency references are updated, and a `moved` history entry preserves the audit trail. Once any task in the sub-graph has been accepted, `move` is refused -- post-dispatch structural mistakes fall back to cancel-and-recreate. See the `quest move` command for the full constraint list.

---

## Input Conventions

### File input

Any flag that accepts free-form text supports reading from a file using the `@file` prefix:

```
quest complete --debrief @debrief.md
quest create --description @desc.md --context @context.md
quest update --handoff @handoff.md
quest fail --debrief @failure-report.md
```

Quest reads the file contents and stores them inline in the task data. The file path is not retained as a reference -- quest owns its data.

This convention applies to: `--debrief`, `--description`, `--context`, `--handoff`, `--note`, `--reason`, `--acceptance-criteria`.

`--title` is excluded because titles are short by design. `--meta` is excluded because its `KEY=VALUE` form is structurally ambiguous for file input (unclear whether `@file` would resolve the value only or the whole pair). ID and enum flags (`--parent`, `--tier`, `--role`, `--tag`, dependency flags) do not take free-form text and are not eligible.

### Path resolution

- Relative paths in `@file` are resolved relative to the caller's current working directory, not the `.quest/` directory
- `@-` reads from stdin, providing a platform-safe escape hatch that works everywhere
- At most one flag per invocation may use `@-`. Stdin is a single byte stream; consuming it twice would yield empty content or block on the second read. A second `@-` on the same invocation returns exit 2 (`usage_error`) with a stderr message naming the flag that already consumed stdin — e.g., `"stdin already consumed by --debrief; at most one @- per invocation"`. Agent retry logic may rely on this contract.
- On Windows, forward slashes are recommended (`@path/to/file.md`). Arguments containing backslashes should be quoted to avoid shell escaping issues

### Size limit

Each `@file` (or `@-` stdin) argument is capped at 1 MiB (1,048,576 bytes) of resolved content. Inputs exceeding the cap are rejected with exit code 2 (usage error) and a stderr message identifying the flag and the observed size. The cap is deliberately generous -- a 1 MiB debrief or description is already beyond the length agents should be writing -- and exists to prevent accidental loading of multi-gigabyte files (e.g., a misaimed log path) into process memory and into the task row. A missing or unreadable `@file` path is also exit code 2 with a stderr message naming the path and the underlying OS error.

---

## Output & Error Conventions

- Output mode is controlled by `--text`; JSON (the agent contract) is the default, `--text` selects a human-readable rendering
- JSON (default) -- structured JSON to stdout, suitable for agent consumption
- Text (`--text`) -- human-readable formatted output to stdout
- No env-var toggle for output mode: Claude Code sessions (and other agents) inherit shell env across many terminals, and a process-wide default would silently corrupt agent output. The choice is per-invocation only
- Warnings and errors always go to stderr regardless of format
- Flat JSON structures preferred over deeply nested
- Consistent types across all commands (durations in seconds, timestamps in ISO 8601)
- **Timestamps are recorded and emitted at second precision**, UTC, `Z`-terminated -- `time.Now().UTC().Format(time.RFC3339)`. Fields affected: `started_at`, `completed_at`, `handoff_written_at`, every `history.timestamp`, every `notes.timestamp`, and the `added_at` on PRs and commits. Sub-second precision is not used: the single-writer model makes collisions at second precision unlikely in practice, and uniform second precision keeps downstream parsing simple
- JSON Lines for streaming output (in json mode)

### Text-mode formatting

Text mode (`--text`) is a human-friendly rendering, not a contract -- see STANDARDS.md §CLI Surface Versioning. The rules below describe the current rendering intent and may evolve without a deprecation cycle; agents MUST NOT parse text output. Because text mode is human-facing, it may carry affordances (row counts, summary footers, relative timestamps) that the JSON agent contract deliberately does not -- adding those affordances to JSON would be a breaking contract change for zero agent benefit.

- No ANSI colors. Humans who want colored rendering pipe quest output through a colorizer. A `--color` flag is deferred until a concrete agent workflow needs it and color rules are pinned here.
- **Helper columns (every column except `title`) use content-aware widths.** Each helper column's width is `max(header_label_width, longest_cell_value_width_in_that_column across the rows being printed)`. The header label length is a floor so headers are never truncated.
- **The `title` column width is allocated from the remaining terminal width** after helpers and inter-column gutters are laid out:
  - **TTY stdout with a known terminal width:** `title_width = term_width - sum(helper_widths) - sum(gutters)`, clamped to an upper bound of 128 (the title field's byte cap per §Field constraints -- rendering beyond that is always padding).
  - **No TTY, or an unknown terminal width** (piped output, redirected stdout, detached terminal): the title column is unbounded. Titles are rendered at their natural length, up to the 128-byte cap.
  - **Narrow terminals where helper columns alone exceed the terminal width:** neither helpers nor title are shortened to fit. The row overflows and the terminal soft-wraps. Narrow terminals are an edge case and wrapped output is readable enough that it is not worth truncating helper content to avoid.
- **Truncation rule.** A cell whose rendered content exceeds its computed column width is cut to `width-3` and suffixed with `...`. Applies to every column, including `title` when the TTY-derived width clamps below the rendered title length.
- **No trailing whitespace on the final column of any row.** Every column except the last is right-padded to its computed width for alignment; the last column is rendered at its natural width with no trailing pad so copy-paste does not pick up invisible whitespace.

### Exit codes

| Code | Meaning                                                                                   |
| ---- | ----------------------------------------------------------------------------------------- |
| 0    | Success                                                                                   |
| 1    | General failure                                                                           |
| 2    | Usage error (bad arguments)                                                               |
| 3    | Resource not found                                                                        |
| 4    | Permission denied                                                                         |
| 5    | Conflict (resource state prevents operation, e.g., terminal state, non-terminal children) |
| 6    | Role denied (elevated command attempted by non-elevated role)                             |
| 7    | Transient failure (write lock unavailable, safe to retry)                                 |

### Error precedence

A single command invocation can trip multiple checks (e.g., a non-elevated worker sends `quest update --tier T3` against a cancelled task ID that does not exist). When more than one condition fails, the reported error class -- and therefore the exit code -- is fixed so that agent retry logic is deterministic. Checks run in this order and the first failure wins:

1. **Role gate** → exit 6 (`role_denied`). For pure-elevated commands (`create`, `batch`, `cancel`, `reset`, `move`, `link`, `unlink`, `tag`, `untag`, `deps`, `list`, `graph`), the role gate fires first: a worker invoking any of these gets exit 6 without the dispatcher consulting the task row at all. This makes role denial uniform regardless of whether the referenced task exists and prevents workers from probing task IDs. The role gate is the framework's context-window and surface-area boundary; it must never leak spec-state details to roles that should not see them.
    - **Mixed-flag carve-out.** `quest update` is dispatched at worker level (so workers can call `--note` / `--pr` / `--handoff`). For `update`, existence (exit 3) fires first, then the role gate re-runs on any elevated flag present. This is the one command where existence precedes the role gate, because the first role gate has already been passed at dispatch (the command itself is worker-accessible).
2. **Existence** → exit 3 (`not_found`). Checked by the transaction's initial `SELECT`. A missing task ID short-circuits every remaining check because no state is known about it.
3. **Permission** → exit 4 (`permission_denied`). Ownership checks for worker commands on tasks owned by a different session. Runs after existence because permission is about the target task, not the caller's role. Only fires when `enforce_session_ownership = true` (see §Role Gating > Session ownership); when the setting is `false` (default), this step is skipped entirely and the precedence ladder proceeds from Existence (exit 3) directly to State (exit 5).
4. **State** → exit 5 (`conflict`). Task status precondition failures (terminal-state gating, non-open parent, non-terminal children, wrong from-status for the transition). Runs after permission so an intruder does not learn the target's status.
5. **Usage** → exit 2 (`usage_error`). Flag-shape problems like an invalid tier string. Runs last because usage validation of free-form fields (notes, descriptions) is pointless if the caller cannot act on the task anyway. Note: `@file` resolution errors (missing file, oversized file, second `@-`) are unrecoverable I/O failures that fire at arg-parse time, before any DB I/O — those also map to exit 2 but are not shape checks and do not participate in this precedence ladder.

Transient lock contention (exit 7) is orthogonal: it is returned whenever `BEGIN IMMEDIATE` times out, regardless of which of the above checks would have fired. Exit 7 is the retryable class; 1-6 are not.

Implementation: every write command routes through `BeginImmediate` and performs the checks in the above order inside the transaction, so the precedence is a property of the code path, not an emergent behavior of check ordering.

### Idempotency

Agents may retry commands after transient failures (exit code 7) or session recovery. The retry behavior of each write command is specified here so agent implementers know which commands are safe to re-execute without side effects.

| Command             | Idempotent | Retry behavior                                                                                                                                       |
| ------------------- | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `accept`            | No         | Fails with exit 5 if already accepted. Crash recovery requires explicit `reset` by the lead                                                          |
| `update --note`     | No         | Always appends a new note. Caller is responsible for deduplication if needed. Fails with exit 5 on cancelled tasks                                   |
| `update --pr`       | Yes        | Duplicate PR URLs are silently ignored. Fails with exit 5 on cancelled tasks                                                                         |
| `update --commit`   | Yes        | Duplicate `BRANCH@HASH` pairs are silently ignored (case-insensitive on the hash, case-sensitive on the branch). Fails with exit 5 on cancelled tasks |
| `update --meta`     | Yes        | Overwrites the value for an existing key; sets it for a new key. Fails with exit 5 on cancelled tasks                                                |
| `update --handoff`  | Yes        | Overwrites previous value. Last write wins. Fails with exit 5 on cancelled tasks                                                                     |
| `complete`          | No         | Fails with exit 5 if already in a terminal state (`completed`, `failed`, or `cancelled`). Terminal states are permanent                              |
| `complete --pr`     | Yes        | Duplicate PR URLs are silently ignored. Fails with exit 5 on cancelled tasks                                                                         |
| `complete --commit` | Yes        | Duplicate `BRANCH@HASH` pairs are silently ignored (case-insensitive on the hash, case-sensitive on the branch). Fails with exit 5 on cancelled tasks |
| `fail`              | No         | Fails with exit 5 if already in a terminal state (`completed`, `failed`, or `cancelled`). Terminal states are permanent                              |
| `fail --pr`         | Yes        | Duplicate PR URLs are silently ignored. Fails with exit 5 on cancelled tasks                                                                         |
| `fail --commit`     | Yes        | Duplicate `BRANCH@HASH` pairs are silently ignored (case-insensitive on the hash, case-sensitive on the branch). Fails with exit 5 on cancelled tasks |
| `cancel`            | Yes        | Returns exit 0 on an already-cancelled task with no state change                                                                                     |
| `link`              | Yes        | Duplicate links of the same type between the same pair return exit 0 with no state change                                                            |
| `unlink`            | Yes        | Removing a non-existent link returns exit 0 with no state change                                                                                     |
| `tag`               | Yes        | Adding an already-present tag is a no-op                                                                                                             |
| `untag`             | Yes        | Removing an absent tag is a no-op                                                                                                                    |

### Write-command output shapes

Every write command emits a small JSON object on stdout on success. Shapes are the agent-facing contract; once pinned here they are governed by `STANDARDS.md` §CLI Surface Versioning. Rich per-command data (full task, history, dependencies) is fetched separately via `quest show`.

| Command            | JSON success shape                                                                         |
| ------------------ | ------------------------------------------------------------------------------------------ |
| `accept`           | `{"id": "...", "status": "accepted"}`                                                      |
| `complete`         | `{"id": "...", "status": "completed"}`                                                     |
| `fail`             | `{"id": "...", "status": "failed"}`                                                        |
| `reset`            | `{"id": "...", "status": "open"}` -- spec'd above                                           |
| `create`           | `{"id": "<new-id>"}` -- the only field; callers `show` for the full task                    |
| `update`           | `{"id": "..."}` -- no echo of which fields changed; callers `show` for post-state          |
| `link`             | `{"task": "...", "target": "...", "link_type": "..."}` -- the edge that was added          |
| `unlink`           | `{"task": "...", "target": "...", "link_type": "..."}` -- the edge that was removed        |
| `tag`              | `{"id": "...", "tags": [...]}` -- full post-state tag list (sorted, lowercase)              |
| `untag`            | `{"id": "...", "tags": [...]}` -- full post-state tag list (sorted, lowercase)              |
| `cancel`           | `{"cancelled": [...], "skipped": [...]}` -- spec'd above                                   |
| `move`             | `{"id": "...", "renames": [...]}` -- spec'd above                                          |
| `batch`            | JSONL ref→id mapping, one `{"ref": "...", "id": "..."}` object per created task -- spec'd above |
| `export`           | `{"dir": "...", "tasks": N, "debriefs": N, "history_entries": N}` -- spec'd below           |

Rules common to the action-ack shapes (`accept`/`complete`/`fail`/`reset`/`create`/`update`):

- All listed fields are always present.
- `id` is the task affected (never `null`).
- `status` on state-transition commands is the post-transition status as a literal string.

For idempotent no-ops, the action-ack still emits the current state: `tag` on an already-tagged task returns `{"id": "...", "tags": [...]}` with the unchanged list, `link` on an already-linked edge returns `{"task": "...", "target": "...", "link_type": "..."}` identifying the existing edge. Agents cannot distinguish "added now" from "already present" from the success body; they can tell from the absence of a history entry if they care.

`deps` and `show` and query commands are not listed here -- they are read commands whose shapes are spec'd per-command under their own section.

Text mode (`--text`) for write commands is a one-liner summarizing the action (e.g., `proj-a1.3 accepted`, `linked proj-a1.3 blocked-by proj-a1.1`). Text mode is not a contract; agents parse JSON.

---

## Task Entity Schema

### Core fields

| Field                 | Set by  | Description                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| --------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`                  | system  | Generated task ID (see Task IDs section)                                                                                                                                                                                                                                                                                                                                                                                                           |
| `title`               | planner | Short description of the task                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `description`         | planner | Full description -- the decomposed unit of work                                                                                                                                                                                                                                                                                                                                                                                                    |
| `context`             | planner | Background information for the worker, injected into the prompt                                                                                                                                                                                                                                                                                                                                                                                    |
| `status`              | system  | Current state (see lifecycle below)                                                                                                                                                                                                                                                                                                                                                                                                                |
| `role`                | planner | Which role is assigned to execute the task. Required for dispatch. Optional on parent tasks -- a roleless parent signals the lead plans to direct-close it; a role can be added later via `quest update --role` if the lead decides to dispatch verification                                                                                                                                                                                       |
| `tier`                | planner | Model tier assignment (see tier list below)                                                                                                                                                                                                                                                                                                                                                                                                        |
| `severity`            | planner | Optional triage severity; one of `critical`, `high`, `medium`, `low`; `null` when unset. Applies to every task. Values are case-sensitive lowercase, matching the casing of `status`; `tier` is uppercase because it is an ID-style label, severity is a word and reads naturally lowercase. Severity is planning metadata parallel to `tier` and `role`: worker-accessible setters (`--note`, `--pr`, `--handoff`) do not include severity, and `quest update --severity` is elevated-only. The enum is enforced at the storage layer via a `CHECK` constraint, matching the precedent set by schema v3 for `status`, `link_type`, and `history.action`           |
| `tags`                | planner | Free-form tags (e.g., `go`, `sql`, `auth`, `concurrency`)                                                                                                                                                                                                                                                                                                                                                                                          |
| `acceptance_criteria` | planner | What must be true for this task to be considered complete. Primarily used on parent tasks to define verification conditions the lead evaluates before closing the group. Convention: use a markdown checklist format (e.g., `- [ ] Integration tests pass\n- [ ] All endpoints return 2xx`) so items are individually addressable in debriefs and retrospectives. Free-form prose is accepted but checklists are preferred for machine-parsability |
| `metadata`            | planner | Arbitrary JSON for planner-defined extensions. The retrospective phase reviews metadata usage across deliveries to identify candidates for promotion to first-class fields                                                                                                                                                                                                                                                                         |

### Relationship fields

| Field          | Set by  | Description                                       |
| -------------- | ------- | ------------------------------------------------- |
| `parent`       | planner | Link to parent task/epic                          |
| `dependencies` | planner | Typed dependency list (see relationships section) |

### Execution fields

| Field                | Set by | Description                                                                                                                                                                                                                                                                                                                                 |
| -------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `owner_session`      | system | Session ID of the agent that accepted the task. Set automatically on `quest accept` from `AGENT_SESSION`. Whether non-owning sessions may write to the task is controlled by `enforce_session_ownership` in `.quest/config.toml` (see §Role Gating > Session ownership): when `true`, only the owning session (or an elevated role) may call `quest update`, `quest complete`, or `quest fail` (non-owning, non-elevated gets exit 4); when `false` (default), the field is recorded for audit and telemetry but not enforced. Cleared on `quest reset` so a new session can accept. `null` when unset                                     |
| `started_at`         | system | ISO 8601 timestamp recorded when the task transitions to `accepted`. Cleared on `quest reset`                                                                                                                                                                                                                                               |
| `completed_at`       | system | ISO 8601 timestamp recorded when the task transitions to `completed` or `failed`. Together with `started_at`, enables duration analysis in retrospectives                                                                                                                                                                                    |
| `prs`                | worker | Links to PRs containing task output (append-only, idempotent)                                                                                                                                                                                                                                                                               |
| `commits`            | worker | Git commits containing task output, as `BRANCH@HASH` records (append-only, idempotent). Parallel to `prs` for work that lands via direct commit rather than (or in addition to) a PR. `commits` and `prs` coexist on a single task -- a PR that squash-merged as a specific commit on master is usefully described by both records                      |
| `notes`              | worker | Array of timestamped progress notes                                                                                                                                                                                                                                                                                                         |
| `handoff`            | worker | Context bridge for session continuity -- what the next session needs to know. Dedicated field (not a note) because it represents the current state checkpoint, not a log entry. Overwrites on each update so `quest show` always surfaces the latest. Survives `quest reset` so the recovering session inherits the prior session's context |
| `handoff_session`    | system | Session ID of the agent that wrote the current `handoff` value. Set automatically from `AGENT_SESSION` when `--handoff` is used. Enables a recovering worker to see that the handoff was written by a different session. `null` when `handoff` is unset                                                                                     |
| `handoff_written_at` | system | ISO 8601 timestamp recorded when the current `handoff` value was written. Enables the lead and recovering workers to assess how fresh the handoff context is relative to the reset. `null` when `handoff` is unset                                                                                                                          |
| `debrief`            | worker | After-action report, submitted at completion or failure                                                                                                                                                                                                                                                                                     |

### History field

| Field     | Set by | Description                                   |
| --------- | ------ | --------------------------------------------- |
| `history` | system | Append-only log of every mutation to the task |

Every mutation to a task appends an entry to the `history` array. Entries are never edited, deleted, or compacted. Event compaction (collapsing or summarizing older entries) is an explicit non-goal: it would silently lose context needed for retrospectives, curator analysis, and crash-reset audit trails -- notably the `handoff_set` `content` field, which exists specifically to preserve context that the live `handoff` field overwrites. When retention of a whole deliverable becomes the concern, the intended answer is purge-after-export, not in-place compaction (see Deferred / Future Concerns).

The one carve-out is referential bookkeeping during `quest move`: when a task's ID changes, the stored `task_id` on existing history rows is updated to preserve the `history ↔ tasks` relationship. Content fields (`action`, `role`, `session`, `reason`, `fields`, `content`, `target`, `link_type`, `old_id`, `new_id`) are never edited; only the foreign-key column. The move itself also appends a `moved` history row recording `old_id`/`new_id`, so the rename is fully recoverable from the audit trail.

```json
{
  "history": [
    {
      "timestamp": "2026-04-14T10:00:00Z",
      "role": "planner",
      "session": "sess-p1a",
      "action": "created"
    },
    {
      "timestamp": "2026-04-14T10:05:00Z",
      "role": "coder",
      "session": "sess-c3f",
      "action": "accepted"
    },
    {
      "timestamp": "2026-04-14T10:30:00Z",
      "role": "coder",
      "session": "sess-c3f",
      "action": "note_added"
    },
    {
      "timestamp": "2026-04-14T10:32:00Z",
      "role": "coder",
      "session": "sess-c3f",
      "action": "handoff_set",
      "content": "Refactored token validation into middleware. Integration test for /protected passes. Still need to wire up refresh-token endpoint -- see auth/refresh.go stub."
    },
    {
      "timestamp": "2026-04-14T10:45:00Z",
      "role": "planner",
      "session": "sess-p1a",
      "action": "reset",
      "reason": "session crashed, retrying at T3"
    },
    {
      "timestamp": "2026-04-14T10:45:01Z",
      "role": "planner",
      "session": "sess-p1a",
      "action": "field_updated",
      "fields": { "tier": { "from": "T2", "to": "T3" } }
    },
    {
      "timestamp": "2026-04-14T10:50:00Z",
      "role": "coder",
      "session": "sess-d7b",
      "action": "accepted"
    },
    {
      "timestamp": "2026-04-14T11:45:00Z",
      "role": "coder",
      "session": "sess-d7b",
      "action": "completed"
    }
  ]
}
```

- `role` is read from `AGENT_ROLE` at the time of the mutation. Recorded as `null` if unset
- `session` is read from `AGENT_SESSION` at the time of the mutation -- this is the unique session ID assigned by vigil, enabling traceability from quest history to specific session logs. Recorded as `null` if unset, which is expected for non-vigil contexts (e.g., humans or planners using the CLI directly)
- `action` identifies the operation: `created`, `accepted`, `completed`, `failed`, `cancelled`, `reset`, `moved`, `note_added`, `pr_added`, `commit_added`, `field_updated`, `linked`, `unlinked`, `tagged`, `untagged`, `handoff_set`. `pr_added` is appended whenever `--pr` adds a URL not already attached to the task -- via `quest update --pr`, `quest complete --pr`, or `quest fail --pr`. `commit_added` is appended whenever `--commit` adds a `BRANCH@HASH` pair not already attached to the task -- via `quest update --commit`, `quest complete --commit`, or `quest fail --commit`. On `complete` / `fail`, the `pr_added` and `commit_added` entries are written alongside the lifecycle entry (`completed` / `failed`) in the same transaction. Idempotent no-op duplicates (the URL or commit pair is already attached) produce no entry, consistent with `tagged`/`untagged`
- `reason` is present for `reset` and `cancelled` -- the lead's annotation of why the task was reset or cancelled. Optional (`null` when the caller did not pass `--reason`).
- `fields` is present only for `field_updated` -- records old and new values for each changed field
- `content` is present for `handoff_set` -- the full text of the handoff that was written. This ensures handoff history is recoverable from the audit log even though the `handoff` field on the task is overwritten on each update. Without this, repeated crash-reset cycles would erase prior handoff context with no trace, violating the retrospective mandate
- `target` and `link_type` are present for `linked` and `unlinked` -- the referenced task ID and the relationship type (e.g., `blocked-by`, `caused-by`)
- `old_id` and `new_id` are present for `moved` -- the task's ID before and after reparenting
- `url` is present for `pr_added` -- the PR URL that was added
- `branch` and `hash` are present for `commit_added` -- the two halves of the commit reference, stored as separate fields rather than a combined `BRANCH@HASH` string so retrospective queries can filter on branch or hash without re-parsing
- For `created`, the payload captures non-default values of the planning fields set at create time: `tier`, `role`, `severity`, `parent`, `tags`, and any initial `dependencies`. Fields left at their defaults are omitted from the payload, not serialized as `null`. This is the retrospective input -- "which tier/role/severity choices produced which outcomes?" -- without requiring a join against the current `tasks` row (which may have been edited after creation)

### Field constraints

The following size caps apply at arg-parse time, before any DB I/O, and return exit code 2 (`usage_error`) with a stderr message naming the flag and the observed byte size.

**`title`: 128 bytes.** The task title is capped at 128 bytes of UTF-8 encoded content. The cap applies to every entry point that sets the title: `quest create --title`, `quest update --title`, and the `title` field in a `quest batch` line. Batch lines report the violation as a per-line JSONL error in the `semantic` phase with code `field_too_long` (see Batch error output).

The 128-byte cap is deliberately tight. Titles are the single-line summary shown in `quest list`, in dependency rows under `quest show`, and in graph output; keeping them short preserves table layouts and keeps the per-task cost in prompt context small. Agents that need to say more belong in `description` and `context`, which are uncapped beyond the 1 MiB `@file` size limit. Raising the cap is backward-compatible; tightening it is not, so the initial cap errs on the tight side.

Bytes, not code points: the check counts the UTF-8 encoded length of the string, consistent with the `@file` byte-based size limit. This avoids Unicode normalization and grapheme-cluster ambiguity.

### Model tiers

The planning agent assigns a tier to each task to control which model executes it. The framework uses this field to select the appropriate model when starting a worker agent.

| Tier | Label      | Use case                                                            |
| ---- | ---------- | ------------------------------------------------------------------- |
| T0   | Tool       | No LLM needed -- handled by a tool or script                        |
| T1   | Minimal    | Classification, extraction, simple reformatting                     |
| T2   | Capable    | Summarization, straightforward code, routine Q&A                    |
| T3   | Strong     | Complex reasoning, nuanced writing, multi-step code                 |
| T4   | Reasoning  | Extended-thinking tasks -- math, formal logic, planning             |
| T5   | Reasoning+ | Max-compute reasoning -- research-grade problems, proofs, hard code |
| T6   | Human      | Human attention -- requires escalation (see framework integration)  |

---

## Status Lifecycle

```
All tasks:     open -> accepted -> completed
                                -> failed
               (at any point)   -> cancelled (planner only)

               accepted -> open (via quest reset, planner only)

Parent tasks (additional path):
               open -> completed (direct-close by lead, no dispatch)
```

- **open** -- task has been created, may or may not be assigned
- **accepted** -- worker has acknowledged and begun work
- **completed** -- worker finished the task and submitted a debrief
- **failed** -- worker could not complete the task
- **cancelled** -- planner aborted the task before completion

Parent tasks (tasks with children) follow the same lifecycle as leaf tasks, with one addition: the lead can transition a parent directly from `open` to `completed` without dispatching a worker, for cases where inline judgment suffices. Dispatched verification uses the standard `open -> accepted -> completed|failed` path -- a verifier agent evaluates the parent's acceptance criteria and closes the task like any other worker. See the Parent Tasks section for the preconditions that govern parent acceptance.

The `open -> accepted` transition is the key diagnostic signal. A task that stays `open` means no agent was dispatched or none started. A task that reaches `accepted` but never completes means the agent started but failed mid-work.

### Crash Recovery

When a worker session crashes mid-task, the task remains in `accepted` status. Recovery is an explicit decision by the lead:

1. Vigil reports the session crash (mechanical outcome)
2. The lead reads the task's handoff to assess the state of the work
3. The lead calls `quest reset` to transition the task back to `open`, optionally updating tier, context, or other fields
4. The lead dispatches a new session via vigil
5. The new session calls `quest show` (which surfaces the handoff from the prior session), then `quest accept`, and proceeds normally

This keeps crash recovery under the lead's judgment -- the lead may choose to reset, cancel, retry-of, or escalate depending on the situation. Quest does not auto-recover.

---

## Parent Tasks

Tasks with children are parents. This is a transient property, not a type -- a task becomes a parent the moment a child is created under it. Parents can either be direct-closed by the lead (for cases where inline judgment suffices) or dispatched to a worker (typically a verifier role) whose job is to evaluate the acceptance criteria and transition the parent to `completed` or `failed`.

### Enforcement rules

Quest enforces three structural constraints on parents:

1. **Accept requires all children in terminal state.** `quest accept` on a parent fails (exit code 5) if any child is not in `completed`, `failed`, or `cancelled`. This precondition ensures that when a verifier accepts a parent, no concurrent child work is in flight -- upholding the one-session-one-task principle. Leaves have no analogous check because they have no children.
2. **Children must be resolved before parent completion.** `quest complete` on a parent fails if any child is not in a terminal state (exit code 5). This applies to both dispatched verification (`accepted -> completed`) and lead direct-close (`open -> completed`). Quest does not derive parent status from children or auto-complete parents -- the close is always an explicit judgment by an agent (verifier or lead).
3. **Parent must be `open` to accept new children.** `quest create --parent ID` and `quest move ID --parent NEW_PARENT` fail (exit code 5) if the prospective parent is in any non-`open` status. For `accepted` parents, this prevents changing the scope of verification while it is in flight. For terminal parents (`completed`, `failed`, `cancelled`), adding children would either falsify the recorded outcome or strand new work under a closed group.

### Closing a parent

Two paths are available:

- **Direct-close (lead).** The lead calls `quest complete` on the parent from `open` status. Useful when acceptance criteria are trivial to evaluate inline (e.g., "all children shipped, nothing further to check") and dispatching a separate session would be overhead.
- **Dispatched verification (verifier role).** The lead assigns a role (typically a verifier) and dispatches a session. The verifier calls `quest accept` (allowed because all children are terminal), evaluates the acceptance criteria, and calls `quest complete` or `quest fail` with a debrief. A failed verification is addressed the same way as any failure -- retry via `retry-of`, follow-up children linked `blocked-by`, or cancellation.

### Role

Role is required on any task being dispatched but is optional on parents. A roleless parent signals that the lead plans to direct-close it; if the lead later decides dispatched verification is needed, a role can be added via `quest update --role`.

### Acceptance criteria

Parent tasks can carry an `acceptance_criteria` field describing what must be true for the group to be considered complete -- not just "children finished" but conditions like "integration tests pass" or "all endpoints return correct status codes." This field is the verifier's brief when dispatched, and the lead's checklist when direct-closing.

For cases where verification scope spans multiple parents or needs a broader frame than a single parent's children, the lead can still create a separate top-level verification task with `blocked-by` dependencies on the relevant tasks. Parent-as-verification-task is the right pattern when verification is scoped to one parent's acceptance criteria; a sibling verification task is the right pattern when the scope is wider.

---

## Graph Limits

### Depth

Maximum nesting depth is 3 levels, enforced at the ID generation layer (see Task IDs section). Three levels maps to epic > task > sub-task. Deeper hierarchies should be modeled as separate top-level tasks connected by `blocked-by` links, which better expresses parallelism and keeps IDs short.

### Fan-out

There is no limit on the number of children per parent task. Quest is designed to handle large deliverables where a single epic may legitimately decompose into dozens of sub-tasks. Fan-out cost is linear -- a wide graph is a larger list but does not degrade ID length, reference density, or structural complexity the way depth does. Imposing an artificial fan-out ceiling would force premature sub-grouping that adds structural noise without solving a real problem. If a planner creates a parent with many children, that is a planning judgment, not a graph pathology.

---

## Relationship Types

Dependencies are typed relationships stored on the dependent task. The first argument to `quest link` is always the task being updated; the flag name encodes the direction of the relationship.

| Type              | Stored on | Meaning                                                                |
| ----------------- | --------- | ---------------------------------------------------------------------- |
| `blocked-by`      | dependent | This task cannot start until the linked task reaches `completed` status |
| `caused-by`       | dependent | This task was caused by work done in the linked task                   |
| `discovered-from` | dependent | This task was discovered during testing of the linked task             |
| `retry-of`        | retry     | This task is a retry of a previously failed task                       |

`blocked-by` is the default type when linking.

### Multi-type links

Multiple links of different types between the same (task, target) pair are permitted. The primary use case is a task that is both `caused-by` and `discovered-from` the same target -- the upstream task both introduced the defect and was being tested when the defect was found. These are distinct retrospective signals: `caused-by` identifies the source of the defect, `discovered-from` identifies the testing context that surfaced it. Collapsing them into a single link loses analytical signal that the retrospective phase depends on.

Duplicate links of the _same_ type between the same pair are silently ignored: the command returns exit code 0 with no state change. This makes `quest link` safe to retry without error handling, which matters for agents that may re-execute a linking sequence after a transient failure. The uniqueness constraint is on (task, target, type), not (task, target).

### Dependency validation

All dependency links are validated on both `quest create` (with dependency flags) and `quest link`. Validation failures return exit code 5 (conflict) with a descriptive stderr message.

**Cycle detection:** `quest link --blocked-by`, `quest create --blocked-by`, and `quest batch` must traverse the full dependency graph before committing an edge. If adding the edge would create a cycle (e.g., A blocked-by B, B blocked-by A), the command fails with exit code 5 and a stderr message describing the cycle path. `quest batch` validates batch-internal references against both each other and the pre-existing graph, not just within the batch.

**Semantic constraints:**

| Relationship type | Target status constraint       |
| ----------------- | ------------------------------ |
| `blocked-by`      | Target must not be `cancelled` |
| `retry-of`        | Target must be `failed`        |
| `caused-by`       | None                           |
| `discovered-from` | None                           |

These constraints prevent silent graph corruption -- a `blocked-by` link to a cancelled task would block the dependent forever, and `retry-of` on a non-failed task is semantically meaningless.

**`blocked-by` resolution:** Only `completed` status satisfies a `blocked-by` dependency. `failed` and `cancelled` do not unblock dependents. If a blocking task fails, the dependent stays blocked until the lead intervenes -- typically by creating a `retry-of` task, unlinking the dependency, or cancelling the dependent. This keeps dispatch decisions under the lead's judgment rather than auto-unblocking tasks whose upstream work was never finished.

### Failed Task Retries

When a task fails, the lead creates a new task with corrective instructions and links it to the failed task via a `retry-of` dependency. The failed task's debrief is preserved as a historical record of what went wrong. The new task carries whatever adjustments are needed -- a higher tier, additional context, directives to use a specific approach or avoid a known pitfall.

This keeps the graph honest: the failed attempt and the successful retry are both visible, enabling retrospective queries like "how often do tasks need retries, and at what tiers?"

The presence of an incoming `retry-of` link is the canonical signal that a failed task has been addressed. A failed task with no incoming `retry-of` link is either abandoned or still needs attention. This convention enables leads to identify unaddressed failures without additional status fields.

Sibling relationships are not modeled explicitly. Tasks that share a parent are siblings. `quest list --parent PARENT-ID` returns all siblings of any task under that parent.

---

## Worker Commands

These commands are available to all agents regardless of role.

---

```
quest show ID [--history]
```

Display full task details including description, context, status, dependencies and their statuses, notes, and handoff from any prior session. Dependencies are automatically included with their title and current status so the worker has immediate context about upstream/downstream tasks without needing to query them separately.

`ID` is required. Quest does not default worker commands to `AGENT_TASK`: that env var is identity metadata (telemetry + correlation), not a CLI convenience. A missing ID returns exit code 2 (`usage_error`).

| Flag        | Description                                             |
| ----------- | ------------------------------------------------------- |
| `--history` | Include the full mutation history (excluded by default) |

History is excluded by default because workers care about current state -- description, context, handoff, and dependency statuses. For long-running tasks with many notes and resets, the history array wastes context window tokens. Elevated roles doing audit or debugging use `--history` to see the full mutation log.

**Field-presence carve-out.** This is the one documented exception to the "all fields always present" rule below: without `--history`, the `history` field is *absent* from the returned object, not serialized as `[]`. The cost argument (skipping history reads entirely) is load-bearing for worker-side command budgets, so the field is omitted rather than fetched-and-emptied.

**JSON output** (default):

```json
{
  "id": "proj-01.3",
  "title": "Auth middleware",
  "description": "Implement the auth middleware that validates JWT tokens on protected routes...",
  "context": "The JWT validation module (proj-01.1) exposes a Validate(token string) function...",
  "status": "open",
  "role": "coder",
  "tier": "T3",
  "severity": null,
  "tags": ["go", "auth"],
  "parent": {"id": "proj-01", "title": "Auth module", "status": "open"},
  "acceptance_criteria": null,
  "metadata": {},
  "owner_session": null,
  "started_at": null,
  "completed_at": null,
  "dependencies": [
    {
      "id": "proj-01.1",
      "title": "JWT validation",
      "status": "completed",
      "link_type": "blocked-by"
    },
    {
      "id": "proj-01.2",
      "title": "Session store",
      "status": "accepted",
      "link_type": "blocked-by"
    }
  ],
  "prs": [],
  "commits": [],
  "notes": [],
  "handoff": null,
  "handoff_session": null,
  "handoff_written_at": null,
  "debrief": null
}
```

All fields are always present in JSON output. Fields with no value are `null` (scalars) or `[]` (arrays) / `{}` (objects) -- never omitted. This guarantees a predictable schema for agent parsing. Fields are ordered as shown above: identity, then planning fields, then relationships, then execution fields.

`parent` is denormalized to `{id, title, status}` when the task has a parent, `null` on root tasks. Dependency objects carry the same three-field task-reference cluster plus a `link_type` naming the relationship (`blocked-by`, `caused-by`, `discovered-from`, `retry-of`). The trio `{id, title, status}` is the canonical shape anywhere a task reference appears in quest output (parent, dependencies, graph edge targets); consumers can treat it as a reusable mini-schema.

**Text output (`--text`)**:

```
proj-01.3 [completed] Auth middleware
    parent     proj-01 [completed] Auth regression sweep
    tags       go, auth
    exec       T3 - coder - sess-d7b
    metadata   priority=high, reviewer=sess-v1a
    started    2026-04-14 10:50Z  (1d ago)
    completed  2026-04-14 11:45Z  (23h ago)

Description
    Implement the auth middleware that validates JWT tokens on
    protected routes. Reject expired, unsigned, and wrong-key tokens.

Context
    The JWT validation module (proj-01.1) exposes Validate(token).

Acceptance criteria
    - [x] Integration tests pass
    - [x] All endpoints return 2xx on valid tokens

Dependencies
    blocked-by  proj-01.1 [completed] JWT validation
    blocked-by  proj-01.2 [completed] Session store

Notes (2)
    2026-04-14 11:15Z  Integration test for /protected passes
    2026-04-14 11:30Z  Refresh-token endpoint wired up

PRs
    https://github.com/foo/bar/pull/42  (2026-04-14 11:30Z)

Commits
    master@a1b2c3d4  (2026-04-14 11:35Z)

Handoff (sess-c3f, 2026-04-14 10:32Z)
    Refactored token validation into middleware. Integration test for
    /protected passes. Still need refresh-token endpoint -- see
    auth/refresh.go stub.

Debrief
    JWT validation + session lookup landed in middleware.go. Coverage
    at 94%. Token-rotation race filed as proj-01.5.
```

Text mode is human-facing and is not a contract -- agents parse JSON. The format is governed by the rules below; any deviation is a renderer bug.

**Header.** The first line is `{id} [{status}] {title}`. The title is emitted verbatim (the 128-byte title cap keeps it within a single reasonable terminal width).

**Metadata cluster.** The header is followed immediately (no blank line) by the metadata rows, indented 4 spaces so they align with every section body below. Keys left-align; values start at the widest-key length + 2 spaces.

| Row         | Rendered when                          | Value                                                                                                          |
| ----------- | -------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `parent`    | `parent` is non-null                   | `{id} [{status}] {title}` -- the task-reference shape                                                          |
| `tags`      | `tags` is non-empty                    | comma-separated, joined with `, `                                                                              |
| `severity`  | `severity` is non-null                 | the literal severity value (`critical`, `high`, `medium`, or `low`)                                            |
| `exec`      | always (`tier` is always set)          | `{tier} - {role} - {session}`, joined with ` - `; trailing nulls drop entirely; mid-string nulls render as `—` |
| `metadata`  | `metadata` is non-empty                | `key=value` pairs comma-joined; nested values render as compact JSON                                           |
| `started`   | `started_at` is non-null               | `{UTC timestamp, minute precision}  ({relative time})`                                                         |
| `completed` | `completed_at` is non-null             | `{UTC timestamp, minute precision}  ({relative time})`                                                         |

A row is omitted entirely when its condition is not met -- `show` never emits a placeholder row like `parent  —`. Rows carry real data or they are absent.

**Sections.** After a single blank line, zero or more sections follow. Each section is a flush-left heading followed by a 4-space-indented body; sections are separated from each other by a single blank line.

| Heading               | Rendered when                                                  | Body                                                                                                                                        |
| --------------------- | -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `Description`         | `description` is non-empty                                     | verbatim, wrapped                                                                                                                           |
| `Context`             | `context` is non-empty                                         | verbatim, wrapped                                                                                                                           |
| `Acceptance criteria` | `acceptance_criteria` is non-empty                             | verbatim, wrapped                                                                                                                           |
| `Dependencies`        | `dependencies` is non-empty                                    | one row per dependency: `{link_type}  {id} [{status}] {title}`                                                                              |
| `Notes (N)`           | `notes` is non-empty (N is the count)                          | one row per note: `{timestamp}  {note body}`, wrapped with hanging indent to the note column                                                |
| `PRs`                 | `prs` is non-empty                                             | one row per PR: `{url}  ({added_at timestamp})`                                                                                             |
| `Commits`             | `commits` is non-empty                                         | one row per commit: `{branch}@{hash}  ({added_at timestamp})` -- parallel to the `PRs` section                                              |
| `Handoff`             | `handoff` is non-null                                          | heading includes a parenthesized `(handoff_session, handoff_written_at)` suffix; body is the handoff content, wrapped                        |
| `Debrief`             | `debrief` is non-null OR `status == "completed"`               | debrief body wrapped; when `status == "completed"` and `debrief` is null the body is the literal `(missing)`                                |
| `History (N)`         | `--history` flag is present (N is the entry count)             | one row per entry (see History layout below)                                                                                                |

Sections whose condition is not met are omitted entirely. No placeholder headings or `(none)` bodies.

**Tag rendering.** Tags render only in the `tags` metadata row on `quest show`. Quest does not promote any tag to an inline rendering convention: `quest graph --text` and dependency rows do not display tags alongside task references, and no tag (including `bug`) earns a dedicated marker in the header or reference rows. Planners choose tag conventions; the renderer stays out.

**Timestamp format.** Minute precision, UTC, `Z`-terminated: `YYYY-MM-DD HH:MMZ`. JSON output retains second precision per the data-type rules; text mode drops seconds because the display-side precision gain does not justify the column width. The `started` and `completed` rows append a parenthesized relative suffix computed against wall clock (`(1d ago)`, `(23h ago)`, `(5m ago)`, `(just now)`). Note, PR, commit, handoff, and history timestamps render absolute only.

**Wrap rules.**

- TTY output wraps prose sections (`Description`, `Context`, `Acceptance criteria`, `Notes`, `Handoff`, `Debrief`) to `min(terminal width, 100)` columns. Wider terminals do not extend prose past 100 columns because line length past ~100 hurts readability.
- Piped output wraps prose sections to 80 columns.
- The metadata cluster and row-oriented sections (`Dependencies`, `PRs`, `Commits`, `History`) are not wrapped. Long lines overflow the terminal; truncation is not used for `show` -- human readers need complete content and accept overflow in exchange.

**History layout.** With `--history`, an additional `History (N)` section is appended at the end:

```
History (10)
    2026-04-14 10:00Z  planner/sess-p1a  created         tier=T2 role=coder tags=[go,auth]
    2026-04-14 10:05Z  coder/sess-c3f    accepted
    2026-04-14 10:30Z  coder/sess-c3f    note_added
    2026-04-14 10:32Z  coder/sess-c3f    handoff_set
    2026-04-14 10:45Z  planner/sess-p1a  reset           "session crashed, retrying at T3"
    2026-04-14 10:45Z  planner/sess-p1a  field_updated   tier: T2 -> T3
    2026-04-14 10:50Z  coder/sess-d7b    accepted
    2026-04-14 11:30Z  coder/sess-d7b    pr_added        https://github.com/foo/bar/pull/42
    2026-04-14 11:35Z  coder/sess-d7b    commit_added    master@a1b2c3d4
    2026-04-14 11:45Z  coder/sess-d7b    completed
```

Per line: `{timestamp}  {role or -}/{session or -}  {action}  [{detail}]`. The action column pads to the widest action string in the section so detail strings align. Per-action detail:

| Action                                                           | Detail                                                                       |
| ---------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `created`                                                        | non-default create-time fields as `key=value`; list fields render as `[a,b]` |
| `accepted`, `completed`, `failed`, `note_added`, `handoff_set`   | no detail -- bodies live in the dedicated sections above                     |
| `cancelled`, `reset`                                             | `"{reason}"` when set, omitted when null                                     |
| `moved`                                                          | `{old_id} -> {new_id}`                                                       |
| `pr_added`                                                       | the PR URL                                                                   |
| `commit_added`                                                   | `{branch}@{hash}` -- the branch and hash rejoined for display only           |
| `field_updated`                                                  | `{field}: {from} -> {to}`, comma-joined across multiple fields               |
| `linked`, `unlinked`                                             | `{link_type} {target}`                                                       |
| `tagged`, `untagged`                                             | the tag value                                                                |

Heavy-content actions (`note_added`, `handoff_set`) carry no inline detail; the dedicated section above holds the body. The History section is a mutation timeline, not a content log.

---

```
quest accept ID
```

Signal that the agent has received the task and begun work. Transitions status from `open` to `accepted`.

`ID` is required; a missing ID returns exit code 2 (`usage_error`).

Accept is strict:

- It only succeeds on tasks in `open` status. Calling `quest accept` on a task that is already `accepted`, `completed`, `failed`, or `cancelled` returns exit code 5 (conflict). This is intentional: if a session crashes and a new session is started for the same task, the lead must explicitly `quest reset` the task before the new session can accept it. This keeps crash recovery as a deliberate decision by the lead, not an implicit side effect.
- On parent tasks, it fails (exit code 5) if any child is not in a terminal state (`completed`, `failed`, or `cancelled`). This precondition ensures a verifier is never accepting a parent while child work is still in flight, upholding the one-session-one-task principle. Leaves have no analogous check.
  **Output shape** for this conflict: stdout carries `{"error": "conflict", "task": "<id>", "non_terminal_children": [{"id": "<child-id>", "status": "<status>"}, ...]}`. The `non_terminal_children` key is a stable contract — agents switch on the field name to extract the blocking IDs. The same key is used on the equivalent `quest complete` conflict body. Stderr carries the standard two-line `quest: conflict: ...` + `quest: exit 5 (conflict)` tail.
- If two agents race to accept the same task, the first writer wins. The second receives exit code 5 (conflict). The accept runs inside a `BEGIN IMMEDIATE` transaction with a SELECT-then-UPDATE in every case -- a leaf accept, like any other status transition, needs to distinguish "task does not exist" (exit 3) from "task exists but is not in `open` status" (exit 5), which an atomic `UPDATE ... WHERE status='open'` with a `RowsAffected` check cannot do (see §Storage > Atomicity). For parents, the transaction additionally verifies the terminal-children precondition before the UPDATE.
- On successful accept, `owner_session` is set from `AGENT_SESSION` and `started_at` is recorded. When `enforce_session_ownership = true` (see §Role Gating > Session ownership), only the owning session (or an elevated role) can call `quest update`, `quest complete`, or `quest fail` on the task; a non-owning, non-elevated session receives exit code 4 (permission denied). When `enforce_session_ownership = false` (default), `owner_session` is recorded but the ownership check is skipped — any caller passing the role gate may proceed.

---

```
quest update ID [flags]
```

Write progress information to the task. Workers can update execution fields. Elevated roles can update any field. `ID` is required; a missing ID returns exit code 2 (`usage_error`).

**Worker flags:**

| Flag                   | Description                                                                         |
| ---------------------- | ----------------------------------------------------------------------------------- |
| `--note "..."`         | Append a timestamped progress note                                                  |
| `--pr "URL"`           | Append a PR link to the task (idempotent -- duplicates ignored)                     |
| `--commit BRANCH@HASH` | Append a git commit reference (repeatable, idempotent -- duplicates ignored)        |
| `--handoff "..."`      | Set handoff context for session continuity (overwrites previous)                    |

**Elevated flags** (require elevated role):

| Flag                          | Description                                              |
| ----------------------------- | -------------------------------------------------------- |
| `--title "..."`               | Update the task title                                    |
| `--description "..."`         | Update the full description                              |
| `--context "..."`             | Update the worker context                                |
| `--tier TIER`                 | Change the model tier                                    |
| `--role ROLE`                 | Change the assigned role                                 |
| `--severity VALUE`            | Set triage severity: `critical`, `high`, `medium`, or `low` |
| `--acceptance-criteria "..."` | Update the verification conditions for parent completion |
| `--meta KEY=VALUE`            | Set a metadata field (repeatable)                        |

All field changes are recorded in the task's history. Flags listed in Input Conventions support `@file` input.

**Terminal-state gating.** On tasks in `completed` or `failed` status, `quest update` accepts only the append/annotation flags: `--note`, `--pr`, `--commit`, and `--meta`. These are either append-only (`--note`, `--pr`, `--commit`) or free-form annotation (`--meta`) and do not retroactively rewrite execution-time state. Any other flag -- `--title`, `--description`, `--context`, `--tier`, `--role`, `--severity`, `--acceptance-criteria`, `--handoff` -- returns exit code 5 (conflict) with a message listing the blocked fields. This applies to both worker and elevated roles. Rationale: fields like `tier`, `role`, and `severity` drive retrospective analytics ("which tiers produced which outcomes?", "how often do critical-severity tasks fail at T2?"); retroactive edits would silently falsify the simplest form of those queries. Planning copy (`title`, `description`, etc.) is preserved in the audit history and has no legitimate post-terminal edit case. `--handoff` is a session-continuity field with no meaning on a task that will not be accepted again. `--pr` and `--commit` are specifically expected after the terminal transition: a worker may land a PR or push a follow-up commit after completion or failure, and those references must be recordable.

**Cancelled tasks reject every `quest update` variant.** `cancelled` is stricter than the other terminal states: `quest update` on a cancelled task -- including `--note`, `--pr`, `--commit`, `--meta`, and `--handoff` -- returns exit code 5 (conflict) with the structured body defined under *In-flight worker coordination* in `quest cancel`. This applies to worker and elevated roles alike. Rationale: the structured conflict on every worker operation is the framework signal that tells vigil to terminate the in-flight worker session; allowing any update to slip through would defeat that signal and let a terminated-but-unaware worker keep writing to a task the planner has already retired. Planner annotations (`--meta`) and debrief-style context belong on the *replacement* task if follow-up is needed, not on the cancelled one. `quest complete` and `quest fail` on a cancelled task are rejected for the same reason.

**Empty values are usage errors.** `--role ""`, `--severity ""`, `--handoff ""`, `--title ""`, `--description ""`, `--context ""`, `--acceptance-criteria ""`, and `--note ""` return exit code 2 (`usage_error`) with a message naming the flag. Empty strings are not a clear-field mechanism; v0.1 does not provide one. `--meta KEY=` (empty value) is also rejected. `--commit` has its own empty-halves rule (see §Commit reference format) -- `--commit master@` (empty hash) and `--commit @abc123` (empty branch) return exit code 2 (`usage_error`) naming the flag. This keeps the common path (passing a real value) unambiguous and avoids a silent-clear footgun for planners building command lines via string templating. If a dedicated clear mechanism is ever needed, it will ship as an explicit `--clear-ROLE` / `--clear-handoff` / `--clear-severity` flag rather than overloading the value flag.

**Severity enum check.** `quest update --severity VALUE` accepts only the four lowercase enum values (`critical`, `high`, `medium`, `low`); any other string, including casing variants like `Critical` or `HIGH`, is rejected with exit code 2 (`usage_error`). Severity change is recorded as a `field_updated` history entry carrying the prior and new values, matching how other planning-field updates render (see §History field).

**Commit reference format.** `--commit` accepts values shaped `BRANCH@HASH` and is **repeatable** on `quest update`, `quest complete`, and `quest fail`. No standalone `quest commit` command exists; repeatability on these three commands covers both the mid-work case (a worker logging commits as they land) and the post-completion case (a late-discovered commit recorded after the terminal transition). The format rules are enforced at arg-parse time, before any DB I/O, and a violation returns exit code 2 (`usage_error`) with a message naming the flag and the offending value.

- **Both halves required.** The value must contain at least one `@` and both sides must be non-empty. `--commit master@` (empty hash) and `--commit @abc123` (empty branch) are rejected with exit 2; so is `--commit abc123` (no separator) and `--commit ""` (empty value). The flag does not treat `BRANCH@` as a clear-field mechanism, consistent with the empty-values policy for `--role`, `--severity`, and related flags.
- **Split on the last `@`.** The parser splits on the **rightmost** `@`, so branch names containing `@` (`release@2025`, `user@feature/x`) parse correctly: `--commit release@2025@abc1234` records branch `release@2025` and hash `abc1234`.
- **Hash shape.** Lowercase hex, 4 or more characters. The regex is `^[0-9a-f]{4,}$`. Quest does not verify the commit exists in any repository and carries no git awareness beyond this shape check -- the field is a record of what the agent reported, not a validation of what is reachable. Uppercase or mixed-case hashes (`ABC123`, `AbC123`) are rejected; agents pass the canonical lowercase form git itself emits.
- **Branch shape.** No validation beyond non-empty. Branches are preserved case-sensitive and rendered verbatim.
- **Dedup for idempotency.** Two `--commit` values on the same task are duplicates when, after lowercasing the hash, they compare byte-equal to an existing record. The branch is compared case-sensitive; the hash comparison is case-insensitive because the hash is already constrained to lowercase hex on write, so the only way a "duplicate with different casing" can arise is across two binaries with drifting validators -- the dedup rule future-proofs against that. Duplicates are silently ignored and produce no `commit_added` history entry, matching the `--pr` idempotency model.
- **Terminal-state gating.** Same rules as `--pr`: accepted on `completed` and `failed` tasks (via `quest update --commit` or the terminal-transition commands themselves), rejected on `cancelled` tasks per the cancelled-rejects-every-variant rule above.
- **Storage.** `commits` is stored durably -- each row carries `branch` and `hash` as separate columns alongside the `added_at` timestamp so retrospective queries can filter on either half without re-parsing. The natural layout is a dedicated `commits` table parallel to however `prs` are stored, with a foreign key back to the task row and an index supporting the dedup lookup on `(task_id, lower(hash), branch)`. The exact schema belongs to the implementation task, not this spec; the note here is that the data is not a JSON blob on the task row, and `commit_added` history rows reference the underlying storage by FK so export can materialize both halves without an ad-hoc parse. Adding `commits` requires a numbered forward-only migration and a `schema_version` bump per STANDARDS.md §Schema Migration Rules; the pre-migration snapshot (§Storage > Pre-migration snapshot) fires as usual.

---

```
quest complete ID --debrief "..." [--pr "URL"] [--commit BRANCH@HASH]
```

Mark the task as completed. Debrief is required -- every completed task must leave a record of what was done and what was learned. `ID` is required; a missing ID returns exit code 2 (`usage_error`).

For leaf tasks, `quest complete` transitions from `accepted` to `completed`. For parent tasks, `quest complete` transitions from either `accepted` (when a dispatched verifier is closing the parent) or `open` (when the lead is direct-closing without dispatch) to `completed`. Completion of a parent fails if any child is not in a terminal state (`completed`, `failed`, or `cancelled`) -- exit code 5. The error message includes the IDs and current statuses of all non-terminal children in both JSON and text output, so the caller can act immediately without a separate `quest children` query. This is a structural integrity constraint, not derived status: quest does not auto-complete parents when children finish. The agent -- verifier or lead -- makes the judgment call, typically after evaluating the parent's acceptance criteria.

On successful completion, `completed_at` is recorded.

| Flag                   | Description                                                                  |
| ---------------------- | ---------------------------------------------------------------------------- |
| `--debrief "..."`      | Free-form after-action report (required)                                     |
| `--pr "URL"`           | Append a PR link to the task (idempotent -- duplicates ignored)              |
| `--commit BRANCH@HASH` | Append a git commit reference (repeatable, idempotent -- duplicates ignored) |

---

```
quest fail ID --debrief "..." [--pr "URL"] [--commit BRANCH@HASH]
```

Mark the task as failed. Debrief is required -- the after-action report should cover what was attempted, why the task failed, and what was learned. `ID` is required; a missing ID returns exit code 2 (`usage_error`). On failure, `completed_at` is recorded (marking the end of execution, whether successful or not).

The debrief can be as brief as a single sentence for simple failures ("upstream API unreachable, no work attempted"). The requirement is that every failure leaves a record, not that the record is lengthy -- even terse debriefs feed the curator's knowledge extraction pipeline and enable retrospective analysis.

`--pr` and `--commit` are accepted on failure for the same reason they are accepted on completion: a worker may have opened a PR or landed a commit before discovering the task could not be finished, and those references are retrospective material regardless of terminal state. Append and idempotency semantics match the `update` and `complete` forms -- duplicate PR URLs are silently ignored, and duplicate `BRANCH@HASH` pairs are silently ignored (case-insensitive on the hash, case-sensitive on the branch; see §Commit reference format).

| Flag                   | Description                                                                  |
| ---------------------- | ---------------------------------------------------------------------------- |
| `--debrief "..."`      | Free-form after-action report (required)                                     |
| `--pr "URL"`           | Append a PR link to the task (idempotent -- duplicates ignored)              |
| `--commit BRANCH@HASH` | Append a git commit reference (repeatable, idempotent -- duplicates ignored) |

---

## Planner Commands (Elevated)

These commands require an elevated role as defined in `.quest/config.toml`.

---

### Task Creation

```
quest create --title "..." [flags]
```

Create a new task.

| Flag                          | Description                                   |
| ----------------------------- | --------------------------------------------- |
| `--title "..."`               | Short task title (required)                   |
| `--description "..."`         | Full description of the work                  |
| `--context "..."`             | Background information for the worker         |
| `--parent ID`                 | Link to parent task/epic                      |
| `--tier TIER`                 | Model tier: T0-T6                             |
| `--role ROLE`                 | Assigned role                                 |
| `--severity VALUE`            | Triage severity: `critical`, `high`, `medium`, or `low` (optional; null when omitted) |
| `--tag TAGS`                  | Comma-separated tags (e.g., `go,auth`)        |
| `--acceptance-criteria "..."` | Verification conditions for parent completion |
| `--meta KEY=VALUE`            | Set a metadata field (repeatable)             |
| `--blocked-by ID`             | Add a blocked-by dependency (repeatable)      |
| `--caused-by ID`              | Add a caused-by link (not repeatable)         |
| `--discovered-from ID`        | Add a discovered-from link (not repeatable)   |
| `--retry-of ID`               | Add a retry-of link (not repeatable)          |

`--blocked-by` is repeatable because a task can legitimately depend on multiple upstream tasks, and that set is discovered incrementally during planning. `--caused-by`, `--discovered-from`, and `--retry-of` each accept a single ID per create: each describes a single originating event (one failure chain, one discovery source, one retry target) and modeling multiple origins is either ambiguous or is better expressed as multiple separate links added via `quest link` after creation.

`--tag` takes a single comma-separated list (e.g., `--tag go,auth,concurrency`) and is **not** repeatable on one invocation -- same shape as the `tags` field in `quest batch` lines and as the `TAGS` argument on `quest tag` / `quest untag`. `quest list --tag` is repeatable, but with different semantics from `create`: comma is AND (intersection within an arm) and repeat is OR (union across arms), giving DNF expressivity for filter queries. The asymmetry is scoped to filter expression and does not propagate back to `create` -- multiple `--tag` on `create` would have no semantic meaning, since a task is either created with a tag set or it isn't.

`quest create --parent ID` fails (exit code 5) if the parent task is not in `open` status (see Parent Tasks > Enforcement rules) or if the parent is already at depth 3 (see Graph Limits section).

`quest create --severity VALUE` accepts only the four enum values (`critical`, `high`, `medium`, `low`). The check is case-sensitive: `Critical`, `CRITICAL`, and any other casing are rejected with exit code 2 (usage error). `--severity ""` is likewise a usage error, consistent with the empty-value policy for other flags -- there is no silent-clear path; if a dedicated clear mechanism is ever needed it ships as a distinct flag.

Flags listed in Input Conventions support `@file` input.

---

```
quest batch FILE [--partial-ok]
```

Create multiple tasks from a JSONL file. Each line is a JSON object matching the `quest create` fields, with additional support for expressing relationships between tasks in the same batch.

Used by the planning agent to create an entire task graph in a single tool call after decomposing a deliverable.

The batch file supports a `ref` field for internal cross-referencing. Tasks can reference other tasks in the same batch by `ref` in their `parent` and `dependencies` fields. External (pre-existing) task IDs are referenced by `id` instead of `ref`. Quest resolves batch references to real task IDs during import.

Both `parent` and `dependencies[].` items accept the same two disambiguated shapes:

- `{"ref": "<batch-local ref>"}` -- resolved against earlier `ref` values in the same batch.
- `{"id": "<task ID>"}` -- resolved against pre-existing tasks in the store.

A bare string in `parent` (e.g., `"parent": "epic-1"`) is shorthand for `{"ref": "epic-1"}` -- ref-only. The shorthand exists so the common case (new task parented under an earlier in-batch task) stays terse. When the planner needs to parent a new task under a pre-existing external task in a single batch, it must use the object form: `{"id": "proj-a1"}`. Mixing the two keys in one reference (e.g., `{"ref": "x", "id": "y"}`) or providing neither is an `ambiguous_reference` parse error -- exactly one of `ref` or `id` must be present. The same disambiguation rule already applies to `dependencies` entries; batch validation phase 2 (Reference) checks that the chosen key resolves to an actual ref/ID. A `ref`-shape target that does not match any earlier batch line is an `unresolved_ref`; an `id`-shape target that does not match any existing task is an `unknown_task_id`.

### Batch file format

```jsonl
{"ref": "epic-1", "title": "Auth module", "tier": "T3", "acceptance_criteria": "Integration tests pass, all endpoints return correct status codes"}
{"ref": "task-1", "title": "JWT validation", "parent": "epic-1", "tier": "T2", "role": "coder", "tags": ["go", "auth"]}
{"ref": "task-2", "title": "Session store", "parent": {"ref": "epic-1"}, "tier": "T2", "role": "coder", "tags": ["go"]}
{"ref": "task-3", "title": "Auth middleware", "parent": "epic-1", "tier": "T3", "role": "coder", "dependencies": [{"ref": "task-1", "link_type": "blocked-by"}, {"ref": "task-2", "link_type": "blocked-by"}]}
{"ref": "task-4", "title": "Fix token leak", "tags": ["bug"], "parent": {"id": "proj-a1"}, "tier": "T2", "dependencies": [{"id": "proj-31", "link_type": "caused-by"}]}
```

The first `task-2` line uses the explicit `{"ref": ...}` form; `task-1` and `task-3` use the bare-string shorthand. `task-4` parents under an external pre-existing task via `{"id": ...}`.

### Batch validation

Every line in the batch is validated through four phases before any tasks are created. Whitespace-only lines (empty or containing only spaces/tabs) are skipped without error, so hand-edited batch files can use blank-line spacing for readability. Error line numbers continue to reflect 1-based positions in the original file, so the planner can locate issues without recomputing offsets after blank lines.

1. **Parse** -- line is valid JSON with required fields
2. **Reference** -- `ref` values are unique across the batch; all `ref` and `id` references in `parent` and `dependencies` resolve (batch refs to earlier lines, external IDs to pre-existing tasks)
3. **Graph** -- no cycles in `blocked-by` edges (within the batch or against the pre-existing graph); no task exceeds depth 3
4. **Semantic** -- per-type target constraints (see Dependency validation)

Validation collects every error it can detect across all lines and all phases, then emits them together. A batch with three malformed lines reports all three parse errors in one response; a batch with a parse error on line 3 and a semantic error on line 7 reports both. The planner fixes everything in one edit rather than discovering errors one round-trip at a time.

A line that fails an earlier phase is excluded from later-phase evaluation -- quest does not fabricate derived errors (e.g., unresolved-ref errors caused by the referenced line being malformed). The planner fixes the upstream error, re-batches, and any previously-masked errors surface in the next pass.

### Batch error handling

By default, a batch is atomic -- it either fully succeeds or fully fails. If any error is reported, no tasks are created. This prevents agents from having to reason about a partially-created graph.

With `--partial-ok`, quest creates the subset of tasks whose lines passed all four phases and whose `parent` and dependency references all resolved to successfully-created tasks (or to pre-existing external tasks). Lines that failed, and lines that depended on failed lines, are reported alongside the created-tasks output so the planner can fix and re-batch only the failures. This mode is useful for large graph decompositions where a single error would otherwise force a full retry at high token cost.

Error reporting is identical in both modes -- the only difference is whether surviving tasks are created. Both modes exit with code 2 if any error is reported, including `--partial-ok` batches where some tasks were created; the non-zero exit signals the planner that follow-up is needed.

**Runtime errors are atomic.** The `--partial-ok` subsetting applies to *validation* failures (the four phases above). The creation step itself runs in a single transaction regardless of mode; if any task insert fails at runtime (constraint violation, lock timeout, internal error), the whole transaction is rolled back and no tasks from that batch are created, even when other lines in the batch had passed all four validation phases. The non-zero exit (7 for lock timeout, 1 for unexpected failures) tells the planner to retry; no partial-success output is produced for runtime failures. This keeps batch outcomes predictable for agents: validation failures are per-line; runtime failures are per-batch.

### Batch error output

Errors are written as JSONL to stderr, one object per line. Each object has:

| Field     | Description                                                      |
| --------- | ---------------------------------------------------------------- |
| `line`    | 1-based line number in the input file (omitted for `empty_file`) |
| `phase`   | `parse`, `reference`, `graph`, or `semantic`                     |
| `code`    | Machine-readable error code (see table below)                    |
| `message` | Human-readable explanation                                       |

Additional fields depend on `code`:

| Code                   | Phase     | Extra fields                                               |
| ---------------------- | --------- | ---------------------------------------------------------- |
| `empty_file`           | parse     | none                                                       |
| `malformed_json`       | parse     | none                                                       |
| `missing_field`        | parse     | `field`                                                    |
| `ambiguous_reference`  | parse     | `field` (`parent` or `dependencies[n]`)                    |
| `duplicate_ref`        | reference | `ref`, `first_line` (line where the ref was first defined) |
| `unresolved_ref`       | reference | `ref`                                                      |
| `unknown_task_id`      | reference | `id`                                                       |
| `cycle`                | graph     | `cycle` (ordered array of refs/IDs forming the cycle)      |
| `depth_exceeded`       | graph     | `depth` (the depth that would result)                      |
| `retry_target_status`  | semantic  | `target`, `actual_status`                                  |
| `blocked_by_cancelled` | semantic  | `target`                                                   |
| `invalid_tag`          | semantic  | `field` (e.g., `tags[2]`), `value` (offending tag)         |
| `invalid_link_type`    | semantic  | `field` (e.g., `dependencies[0].link_type`), `value` (offending string) |
| `invalid_tier`         | semantic  | `field` (`tier`), `value` (offending string)               |
| `invalid_severity`     | semantic  | `field` (`severity`), `value` (offending string)           |
| `field_too_long`       | semantic  | `field` (e.g., `title`), `limit` (byte cap), `observed` (byte count of the offending value) |
| `parent_not_open`      | semantic  | `field` (`parent.id`), `id` (parent id), `actual_status`   |

For cross-line errors (`duplicate_ref`, `cycle`), `line` points to the later line of the pair (or the edge that closed the cycle); the extra fields locate the other party without requiring a second pass.

Array-index notation in `field` (e.g., `tags[2]`, `dependencies[0].link_type`) uses zero-indexed positions within the line's arrays.

Example stderr for a batch with three errors:

```jsonl
{"line": 3, "phase": "parse", "code": "malformed_json", "message": "unexpected token at column 42"}
{"line": 5, "phase": "parse", "code": "missing_field", "field": "title", "message": "required field 'title' is missing"}
{"line": 8, "phase": "parse", "code": "missing_field", "field": "title", "message": "required field 'title' is missing"}
```

### Batch output

Batch output respects the `--text` toggle. In JSON mode (default), quest outputs a JSONL mapping of batch `ref` values to generated task IDs. In `--text` mode, the same mapping is rendered as a table. Either way, the planner can verify the graph and reference tasks going forward.

```json
{"ref": "epic-1", "id": "proj-a1"}
{"ref": "task-1", "id": "proj-a1.1"}
{"ref": "task-2", "id": "proj-a1.2"}
{"ref": "task-3", "id": "proj-a1.3"}
{"ref": "task-4", "id": "proj-a2"}
```

---

### Task Management

```
quest cancel ID [--reason "..."] [-r]
```

Cancel a task. Transitions status to `cancelled`. Only available to elevated roles. Uses `--reason` (not `--debrief`) because cancellation is a planning decision by the lead, not an execution outcome by a worker. If the cancelled task had a worker mid-work, their partial progress is already captured in the task's handoff and notes.

| Flag             | Description                             |
| ---------------- | --------------------------------------- |
| `--reason "..."` | Why the task was cancelled              |
| `-r`             | Recursively cancel all descendant tasks |

`--reason` is optional. An empty value (`--reason ""`) is equivalent to omitting the flag; history records `reason: null`. This is an intentional asymmetry with `quest update`'s free-form text flags: `--reason` annotates a state transition rather than attaching data to the task, so empty and absent are the same signal.

Cancelling a task in `completed` or `failed` status fails with exit code 5 (conflict) -- these terminal states are permanent. Cancelling an already-cancelled task is idempotent: returns exit code 0 with no state change. This is friendlier for scripts and retries.

Without `-r`, the command also fails if the task has non-terminal children (exit code 5). With `-r`, all descendant tasks in `open` or `accepted` status are cancelled with the same reason. Descendants already in a terminal state (`completed`, `failed`, or `cancelled`) are skipped and included in the `skipped` array with their current status. The output reports which tasks were cancelled and which were skipped.

`-r` on a leaf task (no descendants) is not an error: the target task is cancelled normally, `cancelled` lists just the target, and `skipped` is `[]`. `-r` is a no-op *structurally* when no descendants exist but still governs whether the existence of children would have blocked the cancel.

**Output.** In JSON mode (default), output is a single JSON object:

```json
{
  "cancelled": ["proj-a1.3", "proj-a1.3.1"],
  "skipped": [
    {"id": "proj-a1.3.2", "status": "completed"}
  ]
}
```

| Field       | Type             | Description                                                                                                              |
| ----------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `cancelled` | array of strings | IDs of tasks that transitioned to `cancelled` by this call. Always includes the target task when it was cancelled. Order is stable: target first, then descendants by ID |
| `skipped`   | array of objects | Descendants that were not cancelled because they were already in a terminal state. Each object has `id` (string) and `status` (string). Empty array without `-r` or when no descendants were skipped |

Both fields are always present (empty arrays allowed). For an idempotent no-op (cancelling an already-cancelled task), `cancelled` is an empty array and `skipped` is an empty array. In `--text` mode, the output is `cancelled: <id>` per cancelled task followed by `skipped: <id> (<status>)` per skipped task.

### In-flight worker coordination

When a task is cancelled while a worker is mid-flight, quest does not actively notify the worker -- the worker discovers the cancellation on its next quest command. Any call to `quest update`, `quest complete`, or `quest fail` on a cancelled task returns exit code 5 (conflict) with a message and JSON body identifying the cancellation:

```json
{
  "error": "conflict",
  "task": "proj-a1.3",
  "status": "cancelled",
  "message": "task was cancelled"
}
```

The worker cannot modify a cancelled task. Partial progress from the worker's session is already preserved in the task's notes and handoff fields.

Worker termination after cancellation is a framework concern, not a quest concern. Quest records the status change; vigil (or the framework layer managing agent sessions) observes the change and terminates the worker session. Quest does not signal vigil or any external system on status transitions -- this is consistent with the dependency direction where tools do not know about each other's internals and the agent is the integration layer. See Framework Integration Points for the boundary.

---

```
quest reset ID [--reason "..."]
```

Reset a task from `accepted` back to `open` for reassignment. Only available to elevated roles. Used by the lead to handle crash recovery, session hangs, or other situations where a task needs to be re-dispatched.

| Flag             | Description                                       |
| ---------------- | ------------------------------------------------- |
| `--reason "..."` | Why the task is being reset (recorded in history) |

`--reason` is optional. An empty value (`--reason ""`) is equivalent to omitting the flag; history records `reason: null`. Same rationale as `quest cancel --reason`: the flag annotates a state transition, not task data.

The task retains its handoff, notes, and full history. The reset is recorded as a `reset` history entry with the lead's reason. `owner_session` and `started_at` are cleared. Fails if the task is not in `accepted` status (exit code 5).

**Output.** In JSON mode (default), output is a single JSON object:

```json
{"id": "proj-a1.3", "status": "open"}
```

| Field    | Type   | Description                                                     |
| -------- | ------ | --------------------------------------------------------------- |
| `id`     | string | The task ID that was reset                                      |
| `status` | string | The post-reset status; always the literal `"open"` on success   |

Both fields are always present. In `--text` mode, the output is `<id> reset to open`.

---

```
quest move ID --parent NEW_PARENT
```

Reparent a task under a different parent. Only available to elevated roles. The task receives a new ID derived from the new parent's ID namespace (following the standard `{parent}.{N}` convention), and a `moved` history entry records the old and new IDs. All references to the old ID (dependency links from other tasks) are updated to point to the new ID.

| Flag                  | Description                       |
| --------------------- | --------------------------------- |
| `--parent NEW_PARENT` | The new parent task ID (required) |

**Constraints:**

Move is scoped to the planning-and-verification window — after tasks are created but before any of them has been dispatched. Once a worker accepts a task, its ID has been captured by external systems (session logs, vigil state, rite entries, debriefs, git commits) and rewriting it would leave those references stale.

- Fails (exit code 5) if the moved task or any of its descendants has an `accepted` action anywhere in its history. The error message lists the tasks that tripped the check, following the `quest complete`-on-parent convention. The check is on history, not current status, so a task that was accepted and then `reset` back to `open` still blocks the move
- Fails (exit code 5) if the moved task's current parent is in `accepted` status -- a verifier is mid-flight and moving a terminal-state child out would change what they are evaluating
- Fails (exit code 5) if `NEW_PARENT` is not in `open` status, consistent with `quest create --parent` (see Parent Tasks > Enforcement rules)
- Fails (exit code 5) if moving would create a circular parent-child relationship
- Fails (exit code 5) if the move would place the task or any of its descendants beyond the maximum nesting depth of 3 levels
- Recursively moves all descendant tasks, updating their IDs to reflect the new parent namespace. All dependency references (both outgoing from and incoming to the moved sub-graph) are updated to point to the new IDs

This command exists to recover from structural errors caught during planning verification — typically immediately after `quest batch` or a series of individual `quest create` calls — without the cancel-and-recreate workflow, which is prohibitively expensive for large sub-graphs. After dispatch begins, structural errors are handled by cancel-and-recreate; the pre-dispatch scope keeps IDs trustworthy for the external systems that capture them.

**Output.** In JSON mode (default), output is a single JSON object containing the new root ID and a complete rename map:

```json
{
  "id": "proj-b2.1",
  "renames": [
    {"old": "proj-a1.3", "new": "proj-b2.1"},
    {"old": "proj-a1.3.1", "new": "proj-b2.1.1"},
    {"old": "proj-a1.3.2", "new": "proj-b2.1.2"}
  ]
}
```

| Field     | Type             | Description                                                                             |
| --------- | ---------------- | --------------------------------------------------------------------------------------- |
| `id`      | string           | New ID of the task that was moved (the root of the moved sub-graph)                     |
| `renames` | array of objects | Every old→new ID pair, including the moved task and all descendants. Ordered by old ID  |

`renames` is always present and contains at least one entry (the moved task itself). All fields are always present. In `--text` mode, the output is one `OLD → NEW` line per rename, ordered by old ID.

---

### Linking

```
quest link TASK --blocked-by|--caused-by|--discovered-from|--retry-of TARGET
```

Add a typed dependency link to TASK referencing TARGET. The link is stored on TASK. The first argument is always the task being updated. All dependency validation rules apply (see Dependency validation section): cycle detection for `blocked-by`, semantic constraints on target status for all relationship types.

| Flag                       | Description                         |
| -------------------------- | ----------------------------------- |
| `--blocked-by TARGET`      | TASK is blocked by TARGET           |
| `--caused-by TARGET`       | TASK was caused by TARGET           |
| `--discovered-from TARGET` | TASK was discovered from TARGET     |
| `--retry-of TARGET`        | TASK is a retry of TARGET           |

Exactly one relationship flag is required. A bare second positional (`quest link TASK TARGET`) is rejected with exit code 2; agents follow the spec literally and a silent default would hide the link type in history and exports.

```
quest unlink TASK --blocked-by|--caused-by|--discovered-from|--retry-of TARGET
```

Remove a typed dependency link between TASK and TARGET. The removal is recorded in TASK's history with the specific `link_type` removed.

| Flag                       | Description                      |
| -------------------------- | -------------------------------- |
| `--blocked-by TARGET`      | Remove blocked-by link           |
| `--caused-by TARGET`       | Remove caused-by link            |
| `--discovered-from TARGET` | Remove discovered-from link      |
| `--retry-of TARGET`        | Remove retry-of link             |

Exactly one relationship flag is required, mirroring `quest link`. This symmetry makes the command pair learnable (`link --caused-by` to add, `unlink --caused-by` to remove) and prevents accidental removal of links the caller didn't know existed -- consistent with the principle that quest does not make implicit decisions on the agent's behalf.

---

### Tags

Tag management is elevated (see Role Gating). Workers do not modify task metadata on their own tasks -- a worker who identifies a missing tag records the need in their handoff or debrief for the lead to apply.

```
quest tag ID TAGS
```

Add tags to a task. Tags are comma-separated, case-insensitive, stored lowercase.

**Validation.** Each tag must match `^[a-z0-9][a-z0-9-]*$` after lowercasing -- lowercase alphanumerics plus `-`, starting with an alphanumeric. Tags containing whitespace, `.`, `_`, `/`, or other punctuation are rejected with exit code 2 (usage error). The character class is deliberately narrow because tags appear in CLI filters (`quest list --tag`), URL-safe export paths, and shell-quoted scripts; keeping them to a single shell-safe, filename-safe class avoids quoting footguns. Length: 1-32 characters per tag.

```
quest tag proj-a1 go,auth,concurrency
```

```
quest untag ID TAGS
```

Remove tags from a task.

```
quest untag proj-a1 auth,concurrency
```

---

### Queries

Query commands are elevated because workers have exactly one task assigned at session start. A worker does not browse, search, or pick tasks -- the framework dispatches the task and injects its full context into the prompt. If a worker would benefit from information about sibling tasks or the broader graph, the lead adds that information to the task's context before dispatch.

```
quest deps ID
```

List all dependencies for a task, their statuses, and their relationship types. `ID` is required; a missing ID returns exit code 2 (`usage_error`).

---

```
quest list [flags]
```

List tasks with filtering.

| Flag                | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `--status STATUSES` | Filter by status. Comma-separated for OR semantics (e.g., `--status failed,cancelled` or `--status open,accepted`). Repeatable; repeated values union. **Default when omitted:** `open,accepted,completed,failed` (cancelled tasks excluded). Pass an explicit `--status` that includes `cancelled` to list cancelled tasks                                                                                                                                                                                                  |
| `--ready`           | Filter to tasks whose next state transition has no unmet preconditions. For leaves: `status == open` AND all `blocked-by` targets are `completed`. For parents: `status == open` AND all `blocked-by` targets are `completed` AND all children are in a terminal state. The result mixes dispatchable leaves and actionable parents (which the lead may dispatch to a verifier or direct-close); the presence of children distinguishes the two. Composes with other filters (e.g., `quest list --ready --role coder --tier T2`) |
| `--parent IDS`      | Filter by parent ID. Comma-separated for OR semantics (e.g., `--parent proj-a1,proj-a2`). Repeatable; repeated values union                                                                                                                                                                                                                                                                                                                                                                                                    |
| `--blocked-by IDS`  | Filter to tasks that hold a direct `blocked-by` dependency to the given target(s). Comma-separated within one occurrence are AND (task is blocked by **all** listed targets); repeated occurrences are OR (each occurrence is a separate AND-arm). `--blocked-by A,B` matches tasks blocked by both A and B; `--blocked-by A,B --blocked-by C` matches `(A AND B) OR C` (DNF). Filters on **direct** `blocked-by` edges only -- does not traverse transitively. Unknown target IDs match zero rows (no existence check). See "Multi-valued filter semantics" below |
| `--tag TAGS`        | Filter by tag(s). Comma-separated tags within one occurrence are AND (intersection); repeated occurrences are OR (each occurrence is a separate AND-arm; rows match if any arm matches). `--tag a,b` matches tasks tagged with both `a` and `b`; `--tag a,b --tag c,d` matches `(a AND b) OR (c AND d)` (DNF). See "Multi-valued filter semantics" below                                                                                                                                                                       |
| `--role ROLES`      | Filter by role. Comma-separated for OR semantics (e.g., `--role coder,reviewer`). Repeatable; repeated values union                                                                                                                                                                                                                                                                                                                                                                                                            |
| `--tier TIERS`      | Filter by tier. Comma-separated for OR semantics (e.g., `--tier T2,T3`). Repeatable; repeated values union                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `--severity VALUES` | Filter by severity. Comma-separated for OR semantics (e.g., `--severity critical,high`). Repeatable; repeated values union. Accepts only the four enum values (`critical`, `high`, `medium`, `low`); unknown values are rejected with exit code 2 via the same `rejectUnknown` path as the other enum filters. Tasks where `severity IS NULL` are **excluded** from filter matches -- consistent with how `--role coder` excludes null-role tasks. A dedicated flag to query untriaged (null-severity) tasks is deliberately not provided; the need can be revisited when it concretely appears |
| `--columns COLS`    | Comma-separated list of columns to display                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |

**Multi-valued filter semantics.** `--tag` and `--blocked-by` filter on **multi-valued** task fields (a task can hold many tags; a task can hold many `blocked-by` edges). For these flags the within-flag separator and the cross-flag repeat carry **different** semantics: comma within one flag is AND (the row must hold all listed values); repeating the flag is OR (each repetition is a separate AND-arm; the row matches if any arm matches). This lets a single invocation express DNF -- `(A AND B) OR (C AND D)` -- without an external query language. A single-element arm (`--tag X` or `--blocked-by X`) is the degenerate AND-of-one. Tags within an arm and arms across repeats are deduplicated at parse time (`(A∧B) ∨ (A∧B) = (A∧B)`); quest does not otherwise simplify the boolean expression. The other filters (`--status`, `--parent`, `--role`, `--tier`, `--severity`) operate on single-valued fields and use uniform OR/OR (union) semantics -- comma and repeat both mean OR -- because intersection of a single-valued field with two distinct values is always empty.

**Cross-filter composition.** Different filters always compose with AND. With both multi-valued and single-valued filters present, each multi-valued filter contributes its own DNF expression and they AND together: `--tag a,b --tag c,d --status open` resolves to `[(a AND b) OR (c AND d)] AND status=open`.

Edge cases:

- `--tag ""` and `--blocked-by ""` are usage errors (exit 2, `usage_error`), matching the existing empty-value policy for filter flags
- `--tag a,,b` (empty tokens between commas) drops the empty token -- equivalent to `--tag a,b`. No usage error
- `--tag a,a,b` (duplicate within a single arm) is equivalent to `--tag a,b`. Tag arms are sets, not lists
- `--tag a,b --tag a,b` (duplicate arms) is equivalent to `--tag a,b`. The set of arms is also deduplicated
- `--tag a,b --tag a` is **not** equivalent to `--tag a` -- the two arms `(a AND b)` and `a` are distinct disjuncts. The result row set happens to equal "tasks tagged a" because `(a AND b) OR a ≡ a`, but quest does not perform query simplification; the SQL is just less efficient
- Tag character-class validation (`^[a-z0-9][a-z0-9-]*$`, lowercased, 1-32 chars) applies to every tag value in every arm
- `@file` input remains unsupported for `--tag` and `--blocked-by` (per "Input Conventions" -- ID and enum flags don't take free-form text). The new repeat semantics don't change that

**`--blocked-by` filter scope.** The filter matches the **outgoing** `blocked-by` edges held by a row -- "tasks that are blocked by X" -- and not the inverse "tasks that block X". The inverse is not currently expressible via `quest list`; use `quest graph X`. The filter does **not** consult the target's status: a `blocked-by` edge to a `cancelled` or `failed` target still matches. To narrow to "blocked-by edges that actually block dispatch," combine with `--ready` (which requires every `blocked-by` target to be `completed`) or post-process with `quest show`. The flag matches `blocked-by` edges only; `caused-by`, `discovered-from`, and `retry-of` edges are not consulted (they are 1:1 per task and warrant separate filters if ever needed).

**`--blocked-by` example.** Suppose tasks `proj-a4` and `proj-a7` each hold a `blocked-by` link to `proj-a1`, and `proj-a9` holds one to `proj-a2`:

```
quest list --blocked-by proj-a1 --blocked-by proj-a2
```

matches `proj-a4`, `proj-a7`, `proj-a9` (any task blocked by either target). With AND-within-arm:

```
quest list --blocked-by proj-a1,proj-a2
```

matches only tasks holding `blocked-by` links to **both** `proj-a1` and `proj-a2` simultaneously.

Default columns: `id`, `status`, `blocked-by`, `title`.

Available columns: `id`, `title`, `status`, `tier`, `role`, `severity`, `tags`, `parent`, `blocked-by`, `children`.

`severity` is opt-in via `--columns` rather than part of the defaults. The default column set is deliberately narrow to keep the table readable; callers doing triage work add it explicitly (e.g., `--columns id,severity,status,blocked-by,title`). JSON rows emit `severity` as the literal enum value or JSON `null`, matching the rule that enum/planning scalars (`role`, `tier`, `parent`) are never rendered as empty strings.

The `children` column is an array of child task IDs (possibly empty). It is denormalized onto the row so `--ready` consumers can distinguish leaves (empty array) from parents (non-empty) in a single pass, matching the same denormalization used for `blocked-by`.

**Text output (`--text`)** (table):

```
ID          STATUS     BLOCKED-BY           TITLE
proj-a1     open                            Auth module
proj-a1.1   completed                       JWT validation
proj-a1.2   accepted                        Session store
proj-a1.3   open       proj-a1.1,proj-a1.2  Auth middleware

4 tasks
```

A **count footer** follows the table: a blank line, then `N tasks` (or `1 task` when the count is exactly one, or `0 tasks` when the result set is empty). The footer is always emitted in text mode and there is no flag to suppress it. It is a human affordance for comparing list lengths across runs; agents read the JSON array length and so the footer is deliberately **not** mirrored into JSON, which would be a breaking change to an existing contract for zero agent benefit. Singular/plural matters because humans read the line aloud and `1 tasks` reads as a bug.

**JSON output** (default): array of row objects, one per matching task.

```json
[
  {"id": "proj-a1",    "status": "open",      "blocked-by": [],                       "title": "Auth module"},
  {"id": "proj-a1.1",  "status": "completed", "blocked-by": [],                       "title": "JWT validation"},
  {"id": "proj-a1.3",  "status": "open",      "blocked-by": ["proj-a1.1","proj-a1.2"],"title": "Auth middleware"}
]
```

Row shape rules:

- Keys are exactly the requested columns (or the defaults when `--columns` is omitted), with no extra fields. Agents relying on `quest list` should pin `--columns` to the set they consume
- Field order in each row matches the order of `--columns` (or the default-columns order). JSON object key order is preserved on output
- Scalar columns (`id`, `title`, `status`, `tier`, `role`, `parent`) are strings. `role`, `tier`, and `parent` are emitted as JSON `null` when unset, never as the empty string
- `tags` is always a JSON array of strings (possibly empty), never a comma-joined string. The array mirrors the text-mode comma-joined rendering
- `blocked-by` is always a JSON array of task ID strings (possibly empty) -- just IDs, not denormalized `{id,status,title}` objects. The richer edge shape lives in `quest graph`, which is the right tool for structural inspection
- When no tasks match, the output is an empty array `[]` (not `null` and not a missing key). Empty output with exit code 0 is not an error

---

```
quest graph ID
```

Display the dependency graph rooted at a task. `ID` is required; a missing ID returns exit code 2 (`usage_error`).

`ID` may be any task -- root, interior, or leaf. Traversal descends from `ID` through `children` and follows dependency edges outward from tasks in the subtree. It does not traverse up to parents or ancestors; an `--include-ancestors` flag is deferred until a concrete need appears. Any node reached via a dependency edge that is not a descendant of `ID` (siblings, cross-project references, or other out-of-subtree tasks) appears as an unexpanded leaf: it is included in `nodes` so consumers can read its title and status, but its own `children` and outgoing edges are omitted.

Shows parent-child structure, dependency edges with types, and task statuses.

**JSON output** (default): structured adjacency list.

```json
{
  "nodes": [
    {
      "id": "proj-a1",
      "title": "Auth module",
      "status": "open",
      "tier": "T3",
      "role": "coder",
      "severity": null,
      "children": ["proj-a1.1", "proj-a1.2", "proj-a1.3"]
    },
    {
      "id": "proj-a1.1",
      "title": "JWT validation",
      "status": "completed",
      "tier": "T2",
      "role": "coder",
      "severity": null,
      "children": []
    },
    {
      "id": "proj-a1.2",
      "title": "Session store",
      "status": "accepted",
      "tier": "T2",
      "role": "coder",
      "severity": null,
      "children": []
    },
    {
      "id": "proj-a1.3",
      "title": "Auth middleware",
      "status": "open",
      "tier": "T3",
      "role": "coder",
      "severity": "high",
      "children": []
    }
  ],
  "edges": [
    {
      "task": "proj-a1.3",
      "link_type": "blocked-by",
      "target": "proj-a1.1",
      "target_status": "completed"
    },
    {
      "task": "proj-a1.3",
      "link_type": "blocked-by",
      "target": "proj-a1.2",
      "target_status": "accepted"
    }
  ]
}
```

Edge fields mirror the CLI: `task` is the task that holds the link (first arg to `quest link`), `target` is the referenced task. `target_status` is the current status of the target task, denormalized onto the edge so consumers can evaluate dispatch readiness in a single pass without cross-referencing the nodes array. Reads as a sentence: "task proj-a1.3 is blocked-by target proj-a1.1 (which is completed)."

**Design notes:**

- **Two relationship representations.** Parent-child relationships appear as `children` arrays on nodes, while dependency relationships appear in `edges`. This is intentional: parent-child is structural (encoded in the ID, immutable once created), while dependencies are semantic (typed, mutable). They are fundamentally different relationship types and keeping them in separate structures reflects that distinction.
- **External nodes.** "External" is defined relative to the subtree rooted at `ID`, not relative to the project prefix. A sibling like `proj-a1.1` is external when the graph is rooted at `proj-a1.3`, and a cross-project task like `proj-31` referenced via `caused-by` is external at any root. External nodes appear as leaf entries in `nodes` but are not expanded -- their own children and outgoing edges are omitted. Consumers identify external nodes by comparing their ID to the root's: external nodes are not dotted descendants of the root. The ID encoding scheme is a core quest concept that all consumers are required to understand and specifically designed (tradeoffs included) to simplify cases like this and make them unambiguous, so no explicit flag is needed.
- **Edge field naming.** `task` and `target` use quest-specific terminology rather than generic graph terms (e.g., `source`/`target`) to maintain consistency with the CLI surface (`quest link TASK --blocked-by TARGET`).
- **Node fields are structural.** Graph output exists for verifying dependency structure and status, not for displaying full task metadata. Fields like `tags` are intentionally excluded -- they don't contribute to structural verification and are available via `quest list` and `quest show`. `severity` is included on nodes because it is a triage signal a planner reads alongside the graph view (e.g., "what's blocking the one high-severity task in this subtree?"), and is emitted as the literal enum value or JSON `null` per the "all fields always present" rule for JSON output.

**Text output (`--text`)**: human-readable tree. Parent-child structure is conveyed by indentation. Dependency edges are listed under the task that holds the link, matching the JSON model. Every task reference -- node or edge target -- uses the same `{id} [{status}] {title}` shape as `quest show`, so the same scan pattern applies across commands. `severity`, `tier`, and `role` appear on nodes in JSON output but are not rendered in the text tree -- the text tree is intentionally terse for structural scanning, and triage-by-severity views are better served by `quest list --columns id,severity,status,blocked-by,title`.

```
proj-a1 [open] Auth module
  proj-a1.1 [completed] JWT validation
  proj-a1.2 [accepted] Session store
  proj-a1.3 [open] Auth middleware
    blocked-by  proj-a1.1 [completed] JWT validation
    blocked-by  proj-a1.2 [accepted] Session store
```

**Non-root example.** `quest graph proj-a1.3` roots at the leaf. Traversal does not walk up to `proj-a1`. The `blocked-by` targets are siblings, which are external to the subtree, so they appear in `nodes` as unexpanded leaves but are not rendered as tree children in the text form:

```
proj-a1.3 [open] Auth middleware
  blocked-by  proj-a1.1 [completed] JWT validation
  blocked-by  proj-a1.2 [accepted] Session store
```

Used by the planning agent to verify graph correctness after batch task creation, and by humans to understand the structure of a deliverable.

---

## System & Info Commands

`quest init` and `quest version` are always available regardless of role. `quest export` and `quest backup` are elevated commands (they read every task in the project or the whole database) and live in this section because they are system-level archival and recovery tools, not per-task work -- but the role gate still applies.

```
quest init --prefix PREFIX
```

Initialize a quest project in the current directory. Creates `.quest/` directory and `.quest/config.toml`. Requires `--prefix` to set the ID prefix for this project -- see Prefix validation for the allowed format. Fails with exit code 2 (usage error) if `--prefix` is missing or invalid. Fails with exit code 5 (conflict) if `.quest/` already exists in the current directory or any parent.

In JSON mode (default), output is a single JSON object with the resolved workspace path and prefix:

```json
{"workspace": "/abs/path/to/project/.quest", "id_prefix": "proj"}
```

| Field       | Type   | Description                                              |
| ----------- | ------ | -------------------------------------------------------- |
| `workspace` | string | Absolute path to the `.quest/` directory that was created |
| `id_prefix` | string | The prefix recorded in `.quest/config.toml`              |

Both fields are always present and non-empty. In `--text` mode, the output is the bare absolute workspace path followed by a single newline -- no prefix, no framing, no prefix echo. Scripts parsing text mode can read the line directly.

---

```
quest export [--dir PATH]
```

Export the quest database to a human-readable directory structure for inspection, backup, and version control without quest-specific tooling. This fulfills the success criterion that all quest data can be exported to human-readable files.

Only available to elevated roles. Export reads every task in the project, which is a cross-task query; per Role Gating, workers do not browse or query tasks beyond their own. Operators producing archives and planners reviewing project state are the intended callers.

| Flag         | Description                                                                       |
| ------------ | --------------------------------------------------------------------------------- |
| `--dir PATH` | Output directory (default: `<workspace>/quest-export/` — sibling of `.quest/`)    |

The default is resolved relative to the workspace root (where `.quest/` lives), not CWD, so running `quest export` from a subdirectory still places the archive beside `.quest/` and does not mingle human-readable review artifacts with the operational database. An explicit relative `--dir` is resolved against CWD per standard CLI convention.

**Export structure:**

```
quest-export/
  tasks/
    proj-01.json           # Full task data as JSON (all fields)
    proj-01.1.json
    proj-01.2.json
  debriefs/
    proj-01.1.md           # Debrief text as standalone markdown
  history.jsonl            # All history entries as JSONL, chronological
```

Each task JSON file contains the complete task entity (same schema as `quest show --history` output). Debriefs are extracted as standalone markdown files for easy reading. The history JSONL file provides a chronological event stream across all tasks.

The export is the archival and review format; the database is the operational format. Exports are idempotent -- re-running overwrites the output directory.

In JSON mode (default), output is a single JSON object with the resolved output directory and archive counts:

```json
{"dir": "/abs/path/to/quest-export", "tasks": 42, "debriefs": 8, "history_entries": 173}
```

| Field             | Type    | Description                                                                                 |
| ----------------- | ------- | ------------------------------------------------------------------------------------------- |
| `dir`             | string  | Absolute path to the export directory that was written                                      |
| `tasks`           | integer | Number of per-task JSON files written to `tasks/`                                           |
| `debriefs`        | integer | Number of debrief markdown files written to `debriefs/` (tasks with a non-empty debrief)    |
| `history_entries` | integer | Number of rows written to `history.jsonl` (total events across all tasks, chronological)    |

All four fields are always present. The counts let agents sanity-check that the archive contains what was expected before treating it as a durable backup. In `--text` mode, the output is the bare absolute `dir` path followed by a single newline -- matching the `quest init` convention so scripts parsing text mode can read the line directly.

---

```
quest backup --to PATH
```

Write a transaction-consistent snapshot of the quest database to `PATH`. Used by operators and operator-scheduled automation to produce restorable backups of the operational store. See §Backup & Recovery for the broader strategy and restore procedure.

Only available to elevated roles. Backup reads the entire database, which is a cross-task operation; per Role Gating, workers do not query beyond their own task. Operators and operator-run schedulers are the intended callers.

| Flag         | Description                                                                                   |
| ------------ | --------------------------------------------------------------------------------------------- |
| `--to PATH`  | Output path for the database snapshot. Required. Parent directory must exist; it is not created. If the file already exists, it is overwritten. |

`PATH` is resolved relative to CWD per standard CLI convention.

The snapshot is produced via SQLite's online backup API, which yields a transaction-consistent copy without blocking concurrent readers or interrupting WAL-mode writers. The command does not quiesce the workspace; it is safe to run while agents operate on the database.

Alongside `PATH`, quest writes `PATH.config.toml` -- a copy of `.quest/config.toml` in the same directory. Losing `config.toml` (which records the immutable `id_prefix`) would decouple a restored database from external references to task IDs, so the two files are treated as a unit. If writing the sidecar fails, the whole command fails (exit 1) and any partially written `PATH` is removed.

In JSON mode (default), output is a single JSON object:

```json
{"db": "/abs/path/to/backup.db", "config": "/abs/path/to/backup.db.config.toml", "schema_version": 2, "bytes": 40960}
```

| Field            | Type    | Description                                                            |
| ---------------- | ------- | ---------------------------------------------------------------------- |
| `db`             | string  | Absolute path to the database snapshot that was written                |
| `config`         | string  | Absolute path to the config-file sidecar that was written              |
| `schema_version` | integer | The `schema_version` recorded in the snapshot                          |
| `bytes`          | integer | Size of the database snapshot in bytes (config sidecar excluded)       |

All four fields are always present. In `--text` mode, the output is the bare absolute `db` path followed by a single newline -- matching the `quest init` and `quest export` convention so scripts parsing text mode can read the line directly. The sidecar path, schema version, and size are available only in JSON mode (default).

**Idempotency.** Each invocation writes a fresh snapshot at `PATH`, overwriting any prior file. Retrying after a failure is safe -- the prior invocation's output is discarded.

---

```
quest version
```

Print version information.

In JSON mode (default), output is a single JSON object:

```json
{"version": "0.1.0"}
```

The object has exactly one field:

| Field     | Type   | Description                                                                                                                                                                                                             |
| --------- | ------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `version` | string | The binary's version string. Matches the `CHANGELOG.md` entry when built from a release tag; otherwise a `git describe`-style identifier (e.g., `0.1.0-3-gabcdef1-dirty`) or the literal `"dev"` for untagged local builds |

Per the repeated "all fields always present" rule in this spec, the `version` field is always present and is always a non-empty string. Agents parsing `quest version` JSON output may rely on that.

In `--text` mode, the output is the bare version string followed by a newline (no surrounding whitespace, no prefix). Examples:

```
0.1.0
```

Additional informational fields (build commit, build date, Go version, etc.) are deliberately deferred: until there is a concrete agent or tooling consumer for them, keeping the object single-field keeps the contract small and the stability promise simple.

---

## Cross-Tool Linking

Quest stores references to external systems (e.g., memory store entries) as opaque strings in notes, debrief text, handoff text, or metadata. Quest does not resolve or validate these references. The agent is the integration layer between tools, not quest.

---

## Framework Integration Points

These are concerns the framework handles -- not quest commands, but places where the framework interacts with quest:

| Concern             | Framework responsibility                                                                                                                                                                                                                                                                                       |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Agent startup       | Framework reads task tier and role from quest, selects the appropriate model, sets `AGENT_ROLE` (to activate gating), `AGENT_SESSION` (session identity), and `AGENT_TASK` (telemetry correlation) env vars, injects the assigned task ID into the agent's prompt, and starts the agent                       |
| Prompt construction | Framework reads task description and context from `quest show` output and injects them into the agent's prompt template                                                                                                                                                                                        |
| Work dispatch       | Lead uses `quest list --ready` to identify actionable tasks -- leaves to dispatch, parents with a role to dispatch to verifiers, and roleless parents to direct-close -- and declares agent sessions accordingly                                                                                               |
| Recurring tasks     | An external scheduler may call `quest create` or `quest batch` on a cron. Quest has no scheduling concept                                                                                                                                                                                                      |
| Role injection      | Framework sets `AGENT_ROLE` when launching agents                                                                                                                                                                                                                                                              |
| Worker termination  | When a task is cancelled while a worker is mid-flight, vigil observes the status change and terminates the worker session. Quest does not signal vigil -- it records the status transition and vigil detects it via polling or framework-level notification. The termination mechanism is vigil's design space |
| Human escalation    | T6 tasks signify escalations to a human and may (externally) result in a notification, but humans interact with quest using the same CLI commands as agents                                                                                                                                                    |
| Analytics           | Outcome correlation, rework tracking, lead time, and pattern detection are queries over quest data handled by framework tooling -- not quest commands                                                                                                                                                          |

---

## Observability

Quest emits OTEL traces, metrics, and structured logs. The full instrumentation design lives in `docs/OTEL.md`; this section records only the touchpoints where the behavioral spec and telemetry share a contract.

- **Env vars quest reads for telemetry:** `AGENT_ROLE`, `AGENT_TASK`, `AGENT_SESSION`, `TRACEPARENT`, `TRACESTATE` (all set by vigil), plus standard `OTEL_*` variables and `OTEL_GENAI_CAPTURE_CONTENT`. Missing values do not change command behavior -- they surface as empty attributes or, for `AGENT_ROLE`, as the literal `"unset"` in telemetry.
- **Exit code contract:** The exit codes defined in *Output & Error Conventions* (1-7) map one-to-one to a stable `quest.error.class` vocabulary in the OTEL spec (`general_failure`, `usage_error`, `not_found`, `permission_denied`, `conflict`, `role_denied`, `transient_failure`). Changing an exit code in this spec changes the telemetry contract; update both.
- **Lock-contention signal:** The 5-second `BEGIN IMMEDIATE` lock timeout (exit code 7) is the designed threshold for the daemon-upgrade decision. Sustained exit-code-7 rate is the concrete signal for promoting quest to `questd`; the metric is defined in the OTEL spec.
- **History and session identity:** `role` and `session` in history entries (from `AGENT_ROLE` and `AGENT_SESSION`) are the same values surfaced in span attributes (`gen_ai.agent.name`, `dept.session.id`). This lets retrospective queries join history rows to spans by session.
- **No `--no-track`:** Quest's `history` table is append-only and writes only on mutations -- reads produce no history entries regardless of telemetry state. The spec does not carry a `--no-track`-style flag; the OTEL spec documents the rationale.
- **Content capture:** Free-form task content (titles, descriptions, debriefs, handoffs, notes, metadata, tag values, parent IDs) is never emitted as span attributes. Gated content capture per the OTEL spec may surface a truncated subset in span events when `OTEL_GENAI_CAPTURE_CONTENT=true`.

Implementers: see `docs/OTEL.md` for span inventory, metric definitions, attribute schemas, and SDK wiring.

---

## Deferred / Future Concerns

- **Daemon architecture** -- if concurrent write contention exceeds SQLite's single-writer ceiling, introduce a `questd` daemon with a TCP/JSON wire protocol
- **`quest assign`** -- explicit assignment command, if role on create proves insufficient
- **`quest meta get/remove`** -- dedicated metadata read/delete commands, if `quest show` and `quest update --meta` prove insufficient
- **`quest close-all`** -- batch status transitions, if scripting `quest list | quest complete` becomes cumbersome
- **Snapshots / branching** -- tagging and branching the task store for rollback or experimentation
- **`quest diff` / `quest log`** -- history exploration commands for audit and debugging
- **Analytics CLI** -- queries like "tasks at T2 tier fail 3x more on concurrency work" belong in a separate analytics tool that reads quest's data store
- **Debrief processing workflow** -- dedicated commands for marking debriefs as reviewed, if note-based tracking proves insufficient
- **Estimate field** -- first-class effort estimation if the framework develops scheduling/budgeting capabilities that consume it
- **`quest list --no-retry`** -- filter failed tasks that have no incoming `retry-of` link, surfacing unaddressed failures for retrospective triage
- **Automatic dependency migration on retry** -- when a failed task is replaced (via `retry-of` or decomposition into smaller tasks), downstream `blocked-by` dependents must be manually unlinked and relinked to the new task(s). A dedicated command (e.g., `quest retarget`) was considered but deferred: most failures are better addressed by resetting and editing the existing task in-place, and the decomposition case requires per-dependent judgment about where each edge should point. If lead session logs show frequent mechanical unlink/link sequences after task failures, revisit with a purpose-built command informed by the actual usage patterns
- **Purge and retention** -- the quest database is intended as an in-progress work tool, not a permanent record. The long-term archive is the sequence of `quest export` outputs, which are human-readable, backup-friendly, and version-controllable. Once a deliverable is terminal and safely exported, its tasks, history, dependencies, and tags are candidates for purge from the database, keeping the operational store lean and queries fast. A dedicated `quest purge` command, an export-freshness check, a configurable grace period, and a policy for cross-deliverable reference handling are deferred until there is real operational data about deliverable lifetimes and archive usage patterns. This is the intended answer to unbounded history growth in the database -- not event compaction within active tasks, which would violate the audit invariants above

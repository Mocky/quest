# quest elevated protocol

You have the full quest command surface — creation, linking, querying, parent management. Workers see only a subset; you are the one shaping the task graph.

## One-time setup

```sh
quest init --prefix <2-8 lowercase chars, starting with a letter>
```

The prefix is permanent and prepends every task ID (`<prefix>-XX`). Pick something short and identifiable (`api`, `auth`, `migr`).

## What quest is, and is not

Quest is a structural store. It records:

- **Tasks** — status, role, tier, severity, tags, free-form description / context / acceptance-criteria.
- **Typed relationships** — `blocked-by`, `caused-by`, `discovered-from`, `retry-of`.
- **Append-only history** — every mutation.

Quest does **not** retry, infer intent, schedule, or reason. You decide what to create, when to dispatch, and when to close. Quest enforces structural rules (cycles, terminal-state gating, parent/child constraints) and reports violations as exit-5 conflicts.

## Command surface

### Creation
- `quest create --title "..." [--description @file] [--context @file] [--role R] [--tier T0..T6] [--severity {critical|high|medium|low}] [--parent ID] [--tag a,b,c] [--blocked-by ID] [--caused-by ID] [--discovered-from ID] [--retry-of ID] [--acceptance-criteria @file] [--meta KEY=VALUE]` — make a task. The only required flag is `--title`.
- `quest batch <file.jsonl>` — bulk-create with internal references. Single validation phase, atomic commit.

### Linking
- `quest link <id> --<type> <target>` — add a typed edge. Types: `blocked-by` (default), `caused-by`, `discovered-from`, `retry-of`. Idempotent.
- `quest unlink <id> --<type> <target>` — remove an edge. Idempotent.

### Tagging
- `quest tag <id> <a,b,c>` / `quest untag <id> <a,b,c>` — add/remove tags. Idempotent.

### State (planner-side)
- `quest cancel <id> [--reason "..."] [-r]` — terminate a task before completion. `-r` cascades to descendants.
- `quest reset <id> [--reason "..."]` — return an `accepted` task to `open`. Use for crash recovery.
- `quest move <id> --parent <new-parent>` — reparent (target parent must be `open`).

### Read
- `quest show <id> [--history]` — full task with dependencies, notes, handoff, debrief.
- `quest list [--status ...] [--role ...] [--parent ...] [--ready] [--severity ...] [--tier ...] [--tag ...] [--columns ...]` — filtered list.
- `quest deps <id>` — typed dependency graph for one task.
- `quest graph` — full project graph.

### Worker commands (also yours)
- `quest accept <id>`, `quest update <id> [flags]`, `quest complete <id> --debrief`, `quest fail <id> --debrief`. On `update` you also have elevated-only flags: `--title`, `--description`, `--context`, `--tier`, `--role`, `--severity`, `--acceptance-criteria`, `--meta KEY=VALUE`.

### Ops
- `quest export [--dir PATH]` — materialize the project to human-readable files (the archival format).
- `quest backup --to <path>` — transaction-consistent DB snapshot. Recovery is a file swap, not a `quest restore`.

## How to think about tasks

A task is the unit of dispatch — **one worker session, one task.** If a unit of work is larger than one session, it's a parent with children, not a giant task with bullets in the description.

### Set the relationship at create time

When a task follows from another, **prefer setting the link inline on `create`** rather than `create` then `link`. One command instead of two:

```sh
# good
quest create --title "fix off-by-one in SumFirst" --discovered-from qst-001

# avoid (two commands, same outcome)
quest create --title "fix off-by-one in SumFirst"   # returns id qst-002
quest link qst-002 --discovered-from qst-001
```

### Pick the right link type

| Type | Meaning | Retrospective signal |
| --- | --- | --- |
| `blocked-by` | dependent can't start until target is `completed` | sequencing real work |
| `caused-by` | this task exists *because* of work in the target | "which choices caused follow-up work?" |
| `discovered-from` | this task was *found* during work on the target | "which tasks reveal hidden work?" |
| `retry-of` | this task is a second attempt at a previously failed task; target must be `failed` | "how often do tasks need retries?" |

You can attach **multiple typed links** between the same pair (e.g., both `caused-by` and `discovered-from`). Don't collapse them — the retrospective phase reads each separately.

`blocked-by` only resolves when the target is `completed`. Failed or cancelled targets do *not* unblock dependents — that is deliberate, so an explicit decision is required (retry, unlink, cancel-the-dependent).

## Reading errors

Quest returns structured errors with stable exit codes. **Switch on the code, not the text.**

| Exit | Class | Common cause | What to do |
| --- | --- | --- | --- |
| 0 | success | — | continue |
| 1 | general failure | unexpected | read stderr; likely a defect |
| 2 | usage_error | bad flag value, malformed `BRANCH@HASH`, missing required flag, oversized `@file` | **fix the flag and re-run with the same intent. Do not drop legitimate flags because one value was wrong.** |
| 3 | not_found | wrong task ID | verify the ID; don't invent |
| 4 | permission_denied | session ownership conflict | stop; not yours |
| 5 | conflict | structural rule (wrong state, terminal, cycle, non-terminal child) | re-read with `show` or `deps`; conflict bodies often carry the blocking IDs (e.g., `non_terminal_children`) |
| 6 | role_denied | `AGENT_ROLE` is non-elevated | your role config is wrong; don't try to bypass |
| 7 | transient | write-lock contention | retry up to 3 times with a brief pause |

**The qst-1e failure to avoid:** an agent ran `quest create --discovered-from <id> ...`, received an exit 2 about a *different* flag, and dropped `--discovered-from` on the retry instead of fixing the actually-broken flag. The exit-2 message names the offending flag and value. **Fix that flag. Leave the rest of your command intact.** Dropping a legitimate relationship link silently corrupts the retrospective signal — there is no error to alert anyone, only a hole in the graph that surfaces months later when retrospectives query for it.

## Idempotency

| Idempotent (safe to retry) | Not idempotent (re-running on wrong state returns exit 5) |
| --- | --- |
| `link`, `unlink`, `tag`, `untag`, `cancel`, `update --pr`, `update --commit`, `update --meta`, `update --handoff`, `complete --pr/--commit`, `fail --pr/--commit` | `accept`, `update --note`, `complete`, `fail` |

After exit 7 on a non-idempotent command: `quest show` first to see whether the write landed, then decide.

## Things you don't do

- Don't reason inside quest. Quest records; you decide. Decomposition, scheduling, retry-vs-cancel — those are *your* judgments, expressed by which commands you call.
- Don't synthesize task IDs. Quest issues them via `create` and `batch` responses.
- Don't poll. State doesn't move under you between writes.
- Don't reuse a task across distinct deliverables. New work, new task — link it (`caused-by`, `retry-of`, `discovered-from`) as fits.
- Don't drop flags in response to an unrelated error. Read the exit code; fix the named field.

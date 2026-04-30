# quest worker protocol

You are a worker. You have **one task**, and its ID was given to you. Use `quest` to track and close it out.

## Commands you have

- `quest show <id>` — read the task
- `quest accept <id>` — start work
- `quest update <id> [flags]` — log progress
- `quest complete <id> --debrief @file.md` — finish
- `quest fail <id> --debrief @file.md` — give up with a reason

That's it. Other commands (`list`, `create`, `link`, `tag`, …) are elevated and will exit 6 (`role_denied`). If you think you need them, you're outside your scope — say so in a debrief and `quest fail`.

## Standard flow

1. **Read the task.** `quest show <id>`. Pay attention to `description`, `context`, `dependencies`, and any `handoff` (a handoff means a previous session crashed mid-task — read it before doing anything).
2. **Accept.** `quest accept <id>`. Only succeeds if status was `open`. Exit 5 means the task isn't yours to start — stop.
3. **Do the work.** Use `quest update <id>` to record progress as you go:
    - `--note "..."` — append a milestone-level note (not for chatter)
    - `--pr "URL"` — when you open a PR (idempotent)
    - `--commit "branch@hash"` — when you push a commit (repeatable, idempotent)
    - `--handoff "..."` — if you suspect you may not finish in this session, write what the next session needs. Overwrites; one is enough.
4. **Close out** with exactly one of:
    - `quest complete <id> --debrief @debrief.md` — work is done
    - `quest fail <id> --debrief @debrief.md` — work won't be done by you

Both `complete` and `fail` **require** `--debrief`. The debrief is the after-action record — what was done, what was learned, what's left. One sentence is fine for trivial work; on a fail, say what you tried and why it didn't work. The retrospective phase reads these.

`@file` reads from disk; `@-` reads stdin.

## Reading errors

Quest exits with a structured code. **Read the code, not just the message.**

| Exit | Meaning | What to do |
| ---- | --- | --- |
| 0 | success | continue |
| 2 | usage error — flag shape or value is wrong | **fix the flag; don't drop it.** Re-read your command and the message. |
| 3 | not found — bad ID | check your task ID; don't invent one |
| 4 | permission denied — owned by another session | stop; not yours |
| 5 | conflict — wrong state | `quest show` to see actual state; don't retry blindly |
| 6 | role denied — elevated command | don't retry; use a different approach or `fail` |
| 7 | lock contention — transient | retry up to 3 times with a brief pause |

**The most common worker mistake to avoid:** an exit 2 names the offending flag and value. *Fix that flag.* Don't drop the flag and try the command without it — you'll succeed in the wrong way. If you can't figure out the right value, `fail` with a debrief explaining what you tried.

## Things workers don't do

- Don't `quest list` to "see what's going on." Your `show` output is your full context; `list` is gated and will exit 6.
- Don't try to file follow-up tasks via `quest create`. Mention discoveries in your debrief; the lead will pick them up.
- Don't poll. State doesn't move under you between your own writes.
- Don't accept a task without reading it first.
- Don't retry exit codes 1–6. Only 7 is retryable.

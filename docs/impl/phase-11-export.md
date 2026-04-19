# Phase 11 — Export

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 11.1 — `quest export [--dir PATH]`

**Deliverable:** `internal/export/` package + `internal/command/export.go`.

**Spec anchors:** `quest-spec.md` §`quest export` (layout, idempotency).

**Implementation notes:**

- Default output: `filepath.Join(cfg.Workspace.Root, "quest-export")` — always a sibling of `.quest/`, never relative to CWD. A planner running `quest export` from `<workspace>/src/` writes the archive to `<workspace>/quest-export/`, not `<workspace>/src/quest-export/`. This matches spec §`quest export` ("a sibling of `.quest/`"). Layer 4 CLI test runs `quest export` from a subdirectory and asserts the output path is workspace-root-relative.
- Layout exactly as specified: `tasks/{id}.json` for every task, `debriefs/{id}.md` only for tasks that have a non-empty debrief, `history.jsonl` chronologically across all tasks. **Always create the `debriefs/` directory** even when no task has a debrief — this keeps the export layout stable for consumers that pattern-match the on-disk shape. An empty `debriefs/` directory is valid.
- Task JSON uses the same shape as `quest show --history` (i.e., includes the full history array). Contract test asserts this equivalence.
- Idempotent: re-running overwrites. **Track-and-delete-stale pattern**: (1) write every current `tasks/<id>.json` / `debriefs/<id>.md` (using temp-suffix + `os.Rename` for atomicity per file), (2) rewrite `history.jsonl`, (3) collect the set of written task IDs during the write pass, then (4) delete any `tasks/*.json` / `debriefs/*.md` not in the written set. Deletion runs **after** all writes succeed, so a mid-export failure never clobbers the previous archive. Spec §`quest export` says "overwrites the output directory" — interpret as "makes the output directory reflect current state," which means old files for deleted tasks should be removed. Do **not** remove the output directory first (opens a window where the archive is partial) and do **not** ship the temp-suffix-only pattern without delete (stale files from prior runs would accumulate).
- `history.jsonl` entries: one JSON object per history row, ordered by timestamp ascending across all tasks. Apply the same `payload`-flattening rule as `quest show --history` (Task 6.1): merge the stored `payload` JSON into the top level of each entry so `reason`, `fields`, `content`, `target`, `link_type`, `old_id`, `new_id`, `url` appear flat alongside `timestamp`, `role`, `session`, `action`, `task_id`.

**Tests:** Layer 2 contract: layout matches; task JSON field-for-field matches `quest show --history`. Layer 3: idempotency (run twice, diff the tree — should be byte-identical).

**Done when:** export round-trips the full database and produces files that are human-readable and diff-friendly.

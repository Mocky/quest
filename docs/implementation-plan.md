# Quest Implementation Plan (manifest)

**Audience:** coding agents implementing quest from scratch.
**Source docs:** `quest-spec.md` (behavioral contract), `STANDARDS.md`, `OBSERVABILITY.md`, `OTEL.md`, `TESTING.md`, `AGENTS.md`.

When this plan and the spec disagree, the spec wins — update the plan.

This manifest is the entry point. Per-phase detail lives under `impl/`. Cross-cutting rules that apply to every phase live in [impl/cross-cutting.md](impl/cross-cutting.md).

---

## How to use this plan

Work the phases top-to-bottom. Within a phase, tasks are ordered by dependency; later tasks assume earlier ones are done. Every task names:

- **Deliverable** — what must exist on disk when the task is complete.
- **Spec anchors** — sections in `quest-spec.md` (and, where relevant, the standards docs) that govern the task. Re-read these before starting; they contain MUST/MUST NOT rules that override intuition.
- **Implementation notes** — concrete shape of the code: package, types, signatures, edge cases.
- **Tests** — the test layer(s) the task must land with, per `TESTING.md`.
- **Done when** — a checklist that converts the spec into observable outcomes.

Hard rules that apply to every task:

- `internal/config/` is the only package that reads env vars, flags, or `.quest/config.toml`. Every other package accepts config values as parameters. (`STANDARDS.md` Part 1.)
- `internal/telemetry/` is the only package that imports OTEL. Everything else calls `telemetry.RecordX` / `telemetry.CommandSpan`. (`OTEL.md` §8.1, §10.1.)
- Stdout is the data channel, stderr is the diagnostic channel. Never `fmt.Println` for diagnostics; never `fmt.Println` for command results. (`OBSERVABILITY.md` §Anti-Patterns 1-2.)
- Exit codes 1–7 come from `internal/errors.ExitCode(err)`. Never hardcode `os.Exit(5)` anywhere but `main`. (`OBSERVABILITY.md` §Error Handling.)
- No third-party test libraries; table-driven subtests; standard library only. (`TESTING.md` §Philosophy.)
- When a spec field or error code is unclear, stop and resolve it in the spec first. Do not invent behavior. (`AGENTS.md` §What not to do.)

---

## Spec-resolution prerequisites

The plan review surfaced open spec questions. Resolved questions (cancelled-update behavior, `update` precondition precedence, batch `parent` ref/id shape, `quest version` JSON shape, stderr trace-ID enrichment, module path, `CommandSpan` return shape, leaf status-transition atomicity, tag validation rules, enum-filter OR semantics across `--status`/`--role`/`--tier`/`--type`/`--parent`, dependency-flag repeatability, `quest init`/`move`/`cancel`/`reset` JSON shapes, `quest list` JSON row shape, `@file` size limit and error formats, write-command output shapes for `accept`/`complete`/`fail`/`create`/`update`/`link`/`unlink`/`tag`/`untag`, empty-value rejection on `--role`/`--handoff`/`--title`/`--description`/`--context`/`--acceptance-criteria`/`--note`, `--type` transition rejection when `caused-by`/`discovered-from` links exist, `quest graph` requires an explicit ID, `--color` flag dropped from v0.1) are now specified in the relevant anchor doc and the corresponding plan task. No entries are outstanding.

Flag any additional ambiguities discovered during implementation here before coding around them.

### Cross-cutting rules resolved at review time

These are the global rules that multiple tasks below depend on. Re-confirming them up front so each task can reference without restating:

- **Timestamps are second-precision RFC3339 UTC** -- `time.Now().UTC().Format(time.RFC3339)`. Applies to every `started_at`/`completed_at`/`handoff_written_at`, every `history.timestamp`, every `notes.timestamp`, and PR `added_at`. See `quest-spec.md` §Output & Error Conventions.
- **Nullable TEXT columns** (`owner_session`, `handoff`, `handoff_session`, `handoff_written_at`, `role`, `tier`, `acceptance_criteria`, `parent`, `debrief`, `history.role`, `history.session`, etc.) are written with `sql.NullString{}` when the source Go string is empty. `quest show` output emits JSON `null` for unset values, never `""`. Direct SQLite inspection sees `NULL`, not `''`. This rule is enforced at the write path (inside handlers and `store.AppendHistory`), not by a read-side coercion layer.
- **`@-` is single-use per invocation.** `input.Resolve` counts the number of `@-` arguments parsed and rejects the second one with exit 2: `"stdin already consumed by <first-flag>; at most one @- per invocation"`. Applies to `--debrief`, `--note`, `--handoff`, `--description`, `--context`, `--reason`, `--acceptance-criteria` -- any flag in spec §Input Conventions' `@file`-supporting list.
- **`--color` is not a flag in v0.1.** Global flag parsing in Task 4.2 parses `--format` and `--log-level` only. `config.Flags` has two fields. Text-mode rendering in Task 4.3 emits plain text; TTY detection still informs column widths but not color.

---

## Phase index

| Phase | File | Summary | Status |
| ----- | ---- | ------- | ------ |
| 0 | [phase-0-bootstrap.md](impl/phase-0-bootstrap.md) | Repo scaffolding + `quest version` end-to-end | complete |
| 1 | [phase-1-configuration.md](impl/phase-1-configuration.md) | `internal/config/`: workspace discovery, parse, Load/Validate | complete |
| 2 | [phase-2-logging-errors.md](impl/phase-2-logging-errors.md) | slog setup, error sentinels, telemetry no-op shell | complete |
| 3 | [phase-3-storage-foundation.md](impl/phase-3-storage-foundation.md) | SQLite open + WAL + migrations + schema v1 | complete |
| 4 | [phase-4-cli-skeleton.md](impl/phase-4-cli-skeleton.md) | ID generator, dispatcher, output renderer | complete |
| 5 | [phase-5-init.md](impl/phase-5-init.md) | `quest init` | complete |
| 6 | [phase-6-worker-commands.md](impl/phase-6-worker-commands.md) | `show`, `accept`, `update`, `complete`, `fail` | complete |
| 7 | [phase-7-planner-creation.md](impl/phase-7-planner-creation.md) | `create`, dep validator, `batch` | complete |
| 8 | [phase-8-task-management.md](impl/phase-8-task-management.md) | `cancel`, `reset`, `move` | complete |
| 9 | [phase-9-links-tags.md](impl/phase-9-links-tags.md) | `link`/`unlink`, `tag`/`untag` | not started |
| 10 | [phase-10-queries.md](impl/phase-10-queries.md) | `deps`, `list`, `graph` | not started |
| 11 | [phase-11-export.md](impl/phase-11-export.md) | `export` | not started |
| 12 | [phase-12-telemetry.md](impl/phase-12-telemetry.md) | OTEL wiring (real Setup, spans, metrics, content) | not started |
| 13 | [phase-13-tests-ci.md](impl/phase-13-tests-ci.md) | Contract, concurrency, CLI, CI | not started |
| 14 | [phase-14-ship.md](impl/phase-14-ship.md) | Docs pass + changelog + tag v0.1 | not started |

**Completion tracking.** Update the Status column here when a phase moves from `not started` → `in progress` → `complete`. Per-task status lives in the individual task's **Done when** checklist inside each phase file; flip the checklist items as the work lands.

---

## Cross-cutting concerns

See [impl/cross-cutting.md](impl/cross-cutting.md) — history recording, nullable TEXT, timestamps, JSON field presence, error messages, `@file` input, telemetry call sites, precondition-failed events, slog event emission, schema evolution, duration calculation, integration build tags, deliberate deviations, agent discipline.

---

## Cross-file references

Cross-task references in phase files use the form "Task X.Y" — resolve by looking up phase X in the index above. Task numbering is stable across the split; `Task 4.2 step 5` means Phase 4's Task 4.2's step 5 as originally numbered.

---

## Dependency graph of phases

```
Phase 0 ──► Phase 1 ──► Phase 2 ──► Phase 3 ──► Phase 4 ──► Phase 5 ──► Phase 6
                                       │                                   │
                                       │                                   ▼
                                       │                                Phase 7
                                       │                                   │
                                       │                  ┌────────┬──────┴───────┬────────┐
                                       │                  ▼        ▼              ▼        ▼
                                       │              Phase 8  Phase 9       Phase 10  Phase 11
                                       │                  │        │              │        │
                                       ▼                  └────────┴──────┬───────┴────────┘
                                   Phase 12                               ▼
                                                                      Phase 13
                                                                          │
                                                                          ▼
                                                                      Phase 14
```

Edges:

- Phase 0–6 are strictly sequential: each builds infrastructure the next depends on.
- **Phase 7** (create + dep validator + batch) depends on Phase 6 (worker commands) only via the store + ID generator — it does not need `accept`/`complete`/`fail` handlers at runtime. In practice, Phase 6 lands first because it proves out the store.
- **Phase 8** (cancel, reset, move) depends on Phase 7 (it modifies tasks + subgraphs created by `create` / `batch`) but **not** on Phase 9/10/11.
- **Phase 9** (link, tag) depends on Phase 7 only. Independent of Phase 8.
- **Phase 10** (queries) depends on Phase 7 + the ID generator. Independent of Phase 8 and Phase 9 — `list --tag` just returns empty when no tags exist yet.
- **Phase 11** (export) depends on Phase 3 (schema stable) and Phase 6 (full task read path). Independent of Phases 7–10 structurally, but in practice runs after them so there is meaningful data to export.
- **Phase 12** (OTEL) depends only on Task 2.3 (no-op telemetry stub). It can run in parallel with Phases 6–11 because the no-op stubs keep handler signatures stable.
- **Phase 13** (contract + concurrency tests) runs after Phase 11 to catch regressions from both the command surface and the telemetry pass.

**Where two agents can fork:** after Phase 7 lands, Phases 8 / 9 / 10 / 11 can run in any order or in parallel. Phase 12 can run in parallel with any of Phases 6–11.

# Phase 5 — `quest init`

Back to [manifest](../implementation-plan.md) · see [cross-cutting.md](cross-cutting.md).

### Task 5.1 — Implement `quest init --prefix PREFIX`

**Deliverable:** `internal/command/init.go` — creates `.quest/`, writes `config.toml`, opens the DB, applies schema v1, exits 0.

**Spec anchors:** `quest-spec.md` §`quest init`, §Prefix validation, §Tool Identity; `STANDARDS.md` §Config File.

**Implementation notes:**

- Because `init` runs _before_ a workspace exists, it takes a different config-discovery path: do not walk up; operate in CWD. If `.quest/` exists in CWD or any ancestor (use `config.DiscoverRoot` before creating), exit 5 with `quest: conflict: .quest/ already exists at <path>`.
- `init` is dispatched with `RequiresWorkspace=false` (Task 4.2), so `config.Validate` is **not** called. `config.Load` still runs and populates whatever it can from flags/env; the handler only reads `cfg.Agent` (for history attribution) and the flag-supplied `--prefix`.
- Validate `--prefix` via `ids.ValidatePrefix`. Any failure → exit 2 naming the rule.
- Write `.quest/config.toml` with:

  ```toml
  # Role gating — AGENT_ROLE values that unlock elevated commands.
  elevated_roles = ["planner"]

  # Task IDs (immutable for this project's lifetime).
  id_prefix = "<validated prefix>"
  ```

  Use `os.WriteFile` with mode `0o644`. The `.quest/` directory is `0o755`.

- Open the DB at `.quest/quest.db` via `store.Open`, wrap it with `telemetry.WrapStore(s)`, and run migrations inside `telemetry.MigrateSpan` so the migration is still observable:
  ```go
  cwd, err := os.Getwd()
  if err != nil { return err }
  dbPath := filepath.Join(cwd, ".quest/quest.db")
  s, err := store.Open(dbPath)
  if err != nil { return err }
  defer s.Close()
  s = telemetry.WrapStore(s)
  from, err := s.CurrentSchemaVersion(ctx)
  if err != nil { return err }
  migCtx, end := telemetry.MigrateSpan(ctx, from, store.SupportedSchemaVersion)
  applied, err := store.Migrate(migCtx, s)
  end(applied, err)
  if err != nil { return err }
  ```
  **Init derives the DB path locally, not from `cfg.Workspace.DBPath`.** `cfg.Workspace.DBPath` is populated from `cfg.Workspace.Root`, which is empty when `config.DiscoverRoot` does not find a `.quest/` — exactly the state `quest init` runs in. Every other workspace-bound command uses `cfg.Workspace.DBPath`; init is the one carve-out because it is the handler that *creates* the workspace. Compute the path post-mkdir via `filepath.Join(cwd, ".quest/quest.db")` and do not rely on the resolved config value. Because init is the one command where migration runs from inside the handler (rather than via the dispatcher's pre-handler step), `quest.db.migrate` ends up as a *child* of the `execute_tool quest.init` command span — the documented carve-out in `OTEL.md` §8.8. Metrics increment identically to the sibling case. A failed migration leaves the DB at the prior version (empty, in the init case) per spec §Storage — no cleanup needed. Re-running `quest init` after a migration failure is safe because `config.toml` is already written and the DB is either empty or intact. **Note:** init is the only handler-path caller of `telemetry.WrapStore`; every other command receives an already-wrapped store from the dispatcher. Do not copy this pattern into other handlers.
- Output JSON matches spec §`quest init`: `{"workspace": "<absolute path to .quest/>", "id_prefix": "<prefix>"}` — both fields always present. The `workspace` field is the absolute path to the `.quest/` directory itself (e.g. `/abs/path/to/project/.quest`), not the workspace root. `TestInitOutputShape` (Task 13.1) uses `filepath.Base(workspace) == ".quest"` (cross-platform, avoids Windows `\` separators) so a regression that emits the root is caught immediately. In `--format text` the output is the bare absolute `.quest/` path followed by a single newline — no prefix, no framing, no prefix echo (spec §`quest init`).

**Tests:** Layer 4 CLI:

- Happy path: fresh tempdir → exit 0, files created, JSON output contains both fields.
- Bad prefix → exit 2 with the specific rule in the stderr message.
- Re-running init → exit 5 `already exists`.
- Missing `--prefix` → exit 2 usage error.

Layer 3: confirm the DB opens at schema version 1 and all tables exist.

**Done when:** `quest init --prefix tst` in a fresh dir produces a workable `.quest/` that subsequent commands (once implemented) can open.

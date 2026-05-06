package command

import (
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/input"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// updateAck is the spec §Write-command output shapes success body
// (`{"id": "<id>"}`). No echo of which fields changed — callers run
// `quest show` for the post-state.
type updateAck struct {
	ID string `json:"id"`
}

// cancelledConflictBody is the coordination signal emitted on stdout
// when `quest update` / `complete` / `fail` runs against a cancelled
// task. Vigil routes the body to the in-flight worker so it knows to
// terminate. Shape is pinned by spec §In-flight worker coordination.
type cancelledConflictBody struct {
	Error   string `json:"error"`
	Task    string `json:"task"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// updateArgs collects every parsed flag for `quest update`. Pointer
// fields track the set/unset distinction — an unset flag is nil,
// `--title ""` is a non-nil pointer to "" (rejected later as empty).
// Meta and Commits are append-only; multiple `--meta` / `--commit` on
// one invocation produce multiple entries in order.
type updateArgs struct {
	Note               *string
	PR                 *string
	Commits            []batch.Commit
	Handoff            *string
	Title              *string
	Description        *string
	Context            *string
	Tier               *string
	Role               *string
	Severity           *string
	AcceptanceCriteria *string
	Meta               []string
}

// hasElevated reports whether any elevated-only flag is present. Used
// to decide whether the mixed-flag role gate must fire.
func (a updateArgs) hasElevated() bool {
	return a.Title != nil || a.Description != nil || a.Context != nil ||
		a.Tier != nil || a.Role != nil ||
		a.Severity != nil || a.AcceptanceCriteria != nil || len(a.Meta) > 0
}

// blockedOnTerminalState lists every flag that is not --note / --pr /
// --commit / --meta — the append/annotation flags allowed on complete
// and failed tasks per spec §update *Terminal-state gating*. The
// returned slice is used in the exit-5 stderr message so the caller
// sees exactly what is blocked.
func (a updateArgs) blockedOnTerminalState() []string {
	var blocked []string
	if a.Title != nil {
		blocked = append(blocked, "--title")
	}
	if a.Description != nil {
		blocked = append(blocked, "--description")
	}
	if a.Context != nil {
		blocked = append(blocked, "--context")
	}
	if a.Tier != nil {
		blocked = append(blocked, "--tier")
	}
	if a.Role != nil {
		blocked = append(blocked, "--role")
	}
	if a.Severity != nil {
		blocked = append(blocked, "--severity")
	}
	if a.AcceptanceCriteria != nil {
		blocked = append(blocked, "--acceptance-criteria")
	}
	if a.Handoff != nil {
		blocked = append(blocked, "--handoff")
	}
	return blocked
}

// Update writes a task's worker or elevated fields. Mixed-flag
// invocations that mix worker-accessible flags (`--note`, `--pr`,
// `--handoff`) with elevated-only flags (`--title`, `--tier`, ...) are
// dispatched at worker level but re-check the role gate inside the
// handler — the only command in the suite that does.
//
// The precondition ladder is the spec §Error precedence *Mixed-flag
// carve-out*: existence (3) precedes the role gate (6) here, inverting
// the ladder used by every pure-elevated command (see cancel.go,
// move.go, etc., where role-gate-first is the norm). This is
// deliberate, not a bug — the dispatch-time role gate already passed
// because `update` is worker-accessible, so the inner re-check fires
// only on elevated flags present in the mix. Full ladder: existence
// (3) → role gate on elevated flags (6) → ownership (4) →
// terminal-state / cancelled (5) → flag-shape usage (2).
func Update(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	positional, flagArgs := splitLeadingPositional(args)
	parsed, trailing, err := parseUpdateArgs(cfg, stdin, stderr, flagArgs)
	if err != nil {
		return err
	}
	// Trailing positional (ID after flags) is merged with any leading
	// ID; resolveWorkerTaskID rejects >1 as usage.
	positional = append(positional, trailing...)
	id, err := resolveWorkerTaskID("update", positional)
	if err != nil {
		return err
	}

	tx, err := s.BeginImmediate(ctx, store.TxUpdate)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cur, err := loadUpdateState(ctx, tx, id)
	if err != nil {
		return err
	}
	telemetry.RecordTaskContext(ctx, id, cur.tier)

	// Mixed-flag gate — existence already passed above. The dispatcher
	// routes `update` at worker level so workers can call --note/--pr/
	// --handoff; but any elevated flag present re-runs the gate here.
	isElevated := config.IsElevated(cfg.Agent.Role, cfg.Workspace.ElevatedRoles)
	if parsed.hasElevated() {
		telemetry.GateSpan(ctx, cfg.Agent.Role, isElevated)
		if !isElevated {
			slog.InfoContext(ctx, "role gate denied",
				"command", "update",
				"agent.role", cfg.Agent.Role,
				"required", "elevated",
			)
			return fmt.Errorf("update: elevated flags require an elevated role: %w", errors.ErrRoleDenied)
		}
	}

	// Ownership applies after acceptance (spec §accept: "After
	// acceptance, only the owning session ... can call quest update").
	// That covers accepted + terminal (completed/failed/cancelled); only
	// `open` has no owner yet and is skipped. Fires before the cancelled
	// and terminal-state gates so a non-owner learns exit 4, not 5 —
	// spec §Error precedence (permission before state). Skipped entirely
	// when enforce_session_ownership is false (spec §Role Gating >
	// Session ownership); owner_session is still recorded for audit.
	if cfg.Workspace.EnforceSessionOwnership && cur.status != "open" && !isElevated {
		if cur.ownerSession != cfg.Agent.Session {
			telemetry.RecordPreconditionFailed(ctx, "ownership", nil)
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("task is owned by another session: %w", errors.ErrPermission)
		}
	}

	// Terminal-state gating.
	if cur.status == "cancelled" {
		telemetry.RecordPreconditionFailed(ctx, "cancelled", nil)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		body := cancelledConflictBody{
			Error:   "conflict",
			Task:    id,
			Status:  "cancelled",
			Message: "task was cancelled",
		}
		if emitErr := emitCancelledBody(cfg, stdout, body); emitErr != nil {
			return emitErr
		}
		return fmt.Errorf("task was cancelled: %w", errors.ErrConflict)
	}
	if cur.status == "completed" || cur.status == "failed" {
		if blocked := parsed.blockedOnTerminalState(); len(blocked) > 0 {
			telemetry.RecordPreconditionFailed(ctx, "from_status", nil)
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("task is in terminal state (%s); flags not allowed: %s: %w",
				cur.status, strings.Join(blocked, ", "), errors.ErrConflict)
		}
	}

	// Usage validation — fires AFTER state checks so a cancelled task
	// takes precedence over a shape error (spec §Error precedence).
	if err := validateUpdateUsage(parsed); err != nil {
		return err
	}

	// Apply updates.
	now := time.Now().UTC().Format(time.RFC3339)
	if err := applyUpdate(ctx, tx, id, cfg, parsed, cur, now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	if telemetry.CaptureContentEnabled() {
		if parsed.Title != nil && *parsed.Title != "" {
			telemetry.RecordContentTitle(ctx, *parsed.Title)
		}
		if parsed.Description != nil && *parsed.Description != "" {
			telemetry.RecordContentDescription(ctx, *parsed.Description)
		}
		if parsed.Context != nil && *parsed.Context != "" {
			telemetry.RecordContentContext(ctx, *parsed.Context)
		}
		if parsed.AcceptanceCriteria != nil && *parsed.AcceptanceCriteria != "" {
			telemetry.RecordContentAcceptanceCriteria(ctx, *parsed.AcceptanceCriteria)
		}
		if parsed.Note != nil && *parsed.Note != "" {
			telemetry.RecordContentNote(ctx, *parsed.Note)
		}
		if parsed.Handoff != nil && *parsed.Handoff != "" {
			telemetry.RecordContentHandoff(ctx, *parsed.Handoff)
		}
	}
	return output.Emit(stdout, cfg.Output.Text, updateAck{ID: id})
}

// updateFlagSet returns the unparsed FlagSet plus the bound updateArgs
// target. Shared by parseUpdateArgs (handler path) and the help
// dispatcher (passes nil stdin; closures are never invoked).
func updateFlagSet(stdin io.Reader) (*flag.FlagSet, *updateArgs) {
	fs := newFlagSet("update", "ID [flags]",
		"Write progress information to the task. Workers can update execution fields; elevated roles can update any field.")

	parsed := &updateArgs{}
	r := input.NewResolver(stdin)

	setRaw := func(dst **string, flagName string, resolve bool) func(string) error {
		return func(v string) error {
			if resolve {
				resolved, err := r.Resolve(flagName, v)
				if err != nil {
					return err
				}
				v = resolved
			}
			tmp := v
			*dst = &tmp
			return nil
		}
	}

	fs.Func("note", "append a timestamped progress note", setRaw(&parsed.Note, "--note", true))
	fs.Func("pr", "append a PR link", setRaw(&parsed.PR, "--pr", false))
	fs.Func("commit", "append a git commit reference BRANCH@HASH (repeatable)", func(v string) error {
		c, err := batch.ParseCommit("--commit", v)
		if err != nil {
			return fmt.Errorf("update: %w", err)
		}
		parsed.Commits = append(parsed.Commits, c)
		return nil
	})
	fs.Func("handoff", "set handoff context", setRaw(&parsed.Handoff, "--handoff", true))
	fs.Func("title", "update the task title", setRaw(&parsed.Title, "--title", false))
	fs.Func("description", "update the full description", setRaw(&parsed.Description, "--description", true))
	fs.Func("context", "update the worker context", setRaw(&parsed.Context, "--context", true))
	fs.Func("tier", "change the model tier", setRaw(&parsed.Tier, "--tier", false))
	fs.Func("role", "change the assigned role", setRaw(&parsed.Role, "--role", false))
	fs.Func("severity", "change the triage severity", setRaw(&parsed.Severity, "--severity", false))
	fs.Func("acceptance-criteria", "update verification conditions", setRaw(&parsed.AcceptanceCriteria, "--acceptance-criteria", true))
	fs.Func("meta", "set metadata field KEY=VALUE (repeatable)", func(v string) error {
		parsed.Meta = append(parsed.Meta, v)
		return nil
	})
	return fs, parsed
}

// UpdateHelp is the descriptor-side help builder.
func UpdateHelp() *flag.FlagSet { fs, _ := updateFlagSet(nil); return fs }

// parseUpdateArgs consumes args into an updateArgs struct plus the
// leftover positional slice (which carries at most the task ID).
// Free-form flags listed in spec §Input Conventions are passed through
// the per-invocation *input.Resolver; shape errors at arg-parse time
// map to exit 2 before any DB I/O per the plan preamble.
func parseUpdateArgs(cfg config.Config, stdin io.Reader, stderr io.Writer, args []string) (updateArgs, []string, error) {
	_ = cfg
	fs, parsed := updateFlagSet(stdin)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		// Surface the resolver's ErrUsage cleanly; flag package wraps
		// the fn error into its usage output but preserves the chain.
		if stderrors.Is(err, errors.ErrUsage) {
			return updateArgs{}, nil, err
		}
		return updateArgs{}, nil, fmt.Errorf("update: %s: %w", err.Error(), errors.ErrUsage)
	}
	return *parsed, fs.Args(), nil
}

// updateState is the slice of the task row Update needs for the
// precondition ladder plus the pre-update old values for the
// field_updated history entries. Nullable columns arrive as empty Go
// strings — the read path translates SQL NULL back to "".
type updateState struct {
	status             string
	ownerSession       string
	tier               string
	title              string
	description        string
	contextVal         string
	role               string
	severity           string
	acceptanceCriteria string
	metadataJSON       string
}

func loadUpdateState(ctx context.Context, tx *store.Tx, id string) (updateState, error) {
	var (
		cur                             updateState
		owner, tier, role, sev, accCrit sql.NullString
	)
	err := tx.QueryRowContext(ctx,
		`SELECT status, owner_session, tier, title, description, context, role, severity, acceptance_criteria, metadata
		 FROM tasks WHERE id = ?`, id).
		Scan(&cur.status, &owner, &tier, &cur.title, &cur.description, &cur.contextVal, &role, &sev, &accCrit, &cur.metadataJSON)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return updateState{}, fmt.Errorf("%w: task %q", errors.ErrNotFound, id)
		}
		return updateState{}, fmt.Errorf("%w: update: %s", errors.ErrGeneral, err.Error())
	}
	cur.ownerSession = owner.String
	cur.tier = tier.String
	cur.role = role.String
	cur.severity = sev.String
	cur.acceptanceCriteria = accCrit.String
	return cur, nil
}

// validateUpdateUsage enforces empty-value rejection for the listed
// flags per spec §update ("Empty values are usage errors") and the
// --meta KEY=VALUE shape. Runs after state checks so a cancelled task
// takes precedence over a malformed meta pair (spec §Error precedence).
func validateUpdateUsage(a updateArgs) error {
	check := func(flagName string, v *string) error {
		if v != nil && *v == "" {
			return fmt.Errorf("update: %s: empty value rejected: %w", flagName, errors.ErrUsage)
		}
		return nil
	}
	if err := check("--note", a.Note); err != nil {
		return err
	}
	if err := check("--handoff", a.Handoff); err != nil {
		return err
	}
	if err := check("--title", a.Title); err != nil {
		return err
	}
	if a.Title != nil {
		if n := len(*a.Title); n > batch.MaxTitleBytes {
			return fmt.Errorf("update: --title: exceeds %d-byte limit (observed %d bytes): %w",
				batch.MaxTitleBytes, n, errors.ErrUsage)
		}
	}
	if err := check("--description", a.Description); err != nil {
		return err
	}
	if err := check("--context", a.Context); err != nil {
		return err
	}
	if err := check("--role", a.Role); err != nil {
		return err
	}
	if err := check("--severity", a.Severity); err != nil {
		return err
	}
	if err := check("--acceptance-criteria", a.AcceptanceCriteria); err != nil {
		return err
	}
	if a.Tier != nil {
		if err := batch.ValidateTier(*a.Tier); err != nil {
			return fmt.Errorf("update: --tier: %w", err)
		}
	}
	if a.Severity != nil {
		if err := batch.ValidateSeverity(*a.Severity); err != nil {
			return fmt.Errorf("update: --severity: %w", err)
		}
	}
	for _, kv := range a.Meta {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("update: --meta %q: missing '=': %w", kv, errors.ErrUsage)
		}
		if key == "" {
			return fmt.Errorf("update: --meta %q: empty key: %w", kv, errors.ErrUsage)
		}
		if value == "" {
			return fmt.Errorf("update: --meta %q: empty value: %w", kv, errors.ErrUsage)
		}
	}
	return nil
}

// applyUpdate writes every requested change plus the matching history
// entries inside tx. Update order: elevated fields first (one
// field_updated per changed field), then --meta (one field_updated per
// key), then --handoff (upsert + handoff_set), then --note (append +
// note_added), then --pr (insert-or-ignore + pr_added when new). The
// field_updated entries collect old values from `cur` so the history
// payload carries the {from, to} delta.
func applyUpdate(ctx context.Context, tx *store.Tx, id string, cfg config.Config, a updateArgs, cur updateState, now string) error {
	// Elevated scalar fields. Collect `sets` SQL and args, plus emit
	// history per field.
	var (
		sets []string
		argv []any
	)
	addSet := func(column string, oldVal string, newVal string, nullable bool) error {
		if oldVal == newVal {
			return nil
		}
		if nullable && newVal == "" {
			sets = append(sets, column+" = NULL")
		} else {
			sets = append(sets, column+" = ?")
			argv = append(argv, newVal)
		}
		payload := map[string]any{
			"fields": map[string]any{
				column: map[string]any{
					"from": historyNullable(oldVal),
					"to":   newVal,
				},
			},
		}
		return store.AppendHistory(ctx, tx, store.History{
			TaskID:    id,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    store.HistoryFieldUpdated,
			Payload:   payload,
		})
	}

	if a.Title != nil {
		if err := addSet("title", cur.title, *a.Title, false); err != nil {
			return err
		}
	}
	if a.Description != nil {
		if err := addSet("description", cur.description, *a.Description, false); err != nil {
			return err
		}
	}
	if a.Context != nil {
		if err := addSet("context", cur.contextVal, *a.Context, false); err != nil {
			return err
		}
	}
	if a.Tier != nil {
		if err := addSet("tier", cur.tier, *a.Tier, true); err != nil {
			return err
		}
	}
	if a.Role != nil {
		if err := addSet("role", cur.role, *a.Role, true); err != nil {
			return err
		}
	}
	if a.Severity != nil {
		if err := addSet("severity", cur.severity, *a.Severity, true); err != nil {
			return err
		}
	}
	if a.AcceptanceCriteria != nil {
		if err := addSet("acceptance_criteria", cur.acceptanceCriteria, *a.AcceptanceCriteria, true); err != nil {
			return err
		}
	}

	if len(sets) > 0 {
		q := "UPDATE tasks SET " + strings.Join(sets, ", ") + " WHERE id = ?"
		argv = append(argv, id)
		if _, err := tx.ExecContext(ctx, q, argv...); err != nil {
			return fmt.Errorf("%w: update scalar fields: %s", errors.ErrGeneral, err.Error())
		}
	}

	// --meta read-merge-write.
	if len(a.Meta) > 0 {
		merged := map[string]any{}
		if cur.metadataJSON != "" {
			if err := json.Unmarshal([]byte(cur.metadataJSON), &merged); err != nil {
				return fmt.Errorf("%w: update: parse metadata: %s", errors.ErrGeneral, err.Error())
			}
		}
		if merged == nil {
			merged = map[string]any{}
		}
		// Track overwrites in order; if the same key is set twice in
		// one invocation, each call produces its own history entry
		// with the interim `from` value — matching the spec's "every
		// mutation" audit guarantee.
		for _, kv := range a.Meta {
			key, value, _ := strings.Cut(kv, "=")
			old, existed := merged[key]
			merged[key] = value
			fromVal := any(nil)
			if existed {
				fromVal = old
			}
			payload := map[string]any{
				"fields": map[string]any{
					"metadata." + key: map[string]any{
						"from": fromVal,
						"to":   value,
					},
				},
			}
			if err := store.AppendHistory(ctx, tx, store.History{
				TaskID:    id,
				Timestamp: now,
				Role:      cfg.Agent.Role,
				Session:   cfg.Agent.Session,
				Action:    store.HistoryFieldUpdated,
				Payload:   payload,
			}); err != nil {
				return err
			}
		}
		// Canonical JSON — sort keys for stable on-disk representation.
		newJSON, err := marshalSorted(merged)
		if err != nil {
			return fmt.Errorf("%w: update: marshal metadata: %s", errors.ErrGeneral, err.Error())
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET metadata = ? WHERE id = ?`, newJSON, id); err != nil {
			return fmt.Errorf("%w: update metadata: %s", errors.ErrGeneral, err.Error())
		}
	}

	// --handoff upsert + handoff_set history.
	if a.Handoff != nil {
		handoffSess := cfg.Agent.Session
		handoffArg := any(sql.NullString{})
		if handoffSess != "" {
			handoffArg = handoffSess
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET handoff = ?, handoff_session = ?, handoff_written_at = ? WHERE id = ?`,
			*a.Handoff, handoffArg, now, id); err != nil {
			return fmt.Errorf("%w: update handoff: %s", errors.ErrGeneral, err.Error())
		}
		if err := store.AppendHistory(ctx, tx, store.History{
			TaskID:    id,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    store.HistoryHandoffSet,
			Payload:   map[string]any{"content": *a.Handoff},
		}); err != nil {
			return err
		}
	}

	// --note append + note_added history (body lives in notes table,
	// not in the history payload).
	if a.Note != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO notes(task_id, timestamp, body) VALUES (?, ?, ?)`,
			id, now, *a.Note); err != nil {
			return fmt.Errorf("%w: update note: %s", errors.ErrGeneral, err.Error())
		}
		if err := store.AppendHistory(ctx, tx, store.History{
			TaskID:    id,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    store.HistoryNoteAdded,
		}); err != nil {
			return err
		}
	}

	// --pr idempotent append. Primary key (task_id, url) silently
	// swallows duplicates; history fires only on actual inserts.
	if a.PR != nil {
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO prs(task_id, url, added_at) VALUES (?, ?, ?)`,
			id, *a.PR, now)
		if err != nil {
			return fmt.Errorf("%w: update pr: %s", errors.ErrGeneral, err.Error())
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			if err := store.AppendHistory(ctx, tx, store.History{
				TaskID:    id,
				Timestamp: now,
				Role:      cfg.Agent.Role,
				Session:   cfg.Agent.Session,
				Action:    store.HistoryPRAdded,
				Payload:   map[string]any{"url": *a.PR},
			}); err != nil {
				return err
			}
		}
	}

	// --commit idempotent append (repeatable). UNIQUE index on
	// (task_id, branch, lower(hash)) silently swallows duplicates;
	// history fires only when a row was actually inserted.
	for _, c := range a.Commits {
		if err := appendCommit(ctx, tx, id, cfg, c, now); err != nil {
			return err
		}
	}

	return nil
}

// appendCommit writes one commit record plus its commit_added history
// row. The INSERT uses OR IGNORE so the UNIQUE (task_id, branch,
// lower(hash)) index absorbs duplicate BRANCH@HASH values — spec
// §Commit reference format pins dedup as silent and produces no
// history entry. Shared across update.go and close.go so dedup
// semantics cannot drift between the update/complete/fail forms.
func appendCommit(ctx context.Context, tx *store.Tx, taskID string, cfg config.Config, c batch.Commit, now string) error {
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO commits(task_id, branch, hash, added_at) VALUES (?, ?, ?, ?)`,
		taskID, c.Branch, c.Hash, now)
	if err != nil {
		return fmt.Errorf("%w: update commit: %s", errors.ErrGeneral, err.Error())
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	return store.AppendHistory(ctx, tx, store.History{
		TaskID:    taskID,
		Timestamp: now,
		Role:      cfg.Agent.Role,
		Session:   cfg.Agent.Session,
		Action:    store.HistoryCommitAdded,
		Payload:   map[string]any{"branch": c.Branch, "hash": c.Hash},
	})
}

// historyNullable returns nil for "" so the history payload's `from`
// field emits JSON null for a previously-unset field. Non-empty
// values pass through unchanged.
func historyNullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// marshalSorted produces canonical JSON with keys in sorted order.
// Stable output means the tasks.metadata column is byte-identical for
// equal value sets — keeps migrations + diff tooling simpler.
func marshalSorted(m map[string]any) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return "", err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(m[k])
		if err != nil {
			return "", err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.String(), nil
}

// emitCancelledBody writes the cancelled coordination body to stdout
// in the active output mode. Shape is contract-pinned (spec §update
// *In-flight worker coordination*); text mode emits a short summary.
func emitCancelledBody(cfg config.Config, stdout io.Writer, body cancelledConflictBody) error {
	if cfg.Output.Text {
		_, err := fmt.Fprintf(stdout, "conflict: %s was cancelled\n", body.Task)
		return err
	}
	enc := json.NewEncoder(stdout)
	return enc.Encode(body)
}

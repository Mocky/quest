package command

import (
	"context"
	"database/sql"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/input"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// closeAck is the spec §Write-command output shapes success body for
// both complete and fail. The status field carries the post-transition
// state as a literal string.
type closeAck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// closeAction holds the per-command knobs that differ between complete
// and fail: the tx kind (separate histograms in OTEL.md §4.3 / §5.3),
// the history action, the post-state string, and whether `open` is a
// valid from-status (complete accepts it for parent direct-close;
// fail does not).
type closeAction struct {
	name          string
	txKind        store.TxKind
	historyAction store.HistoryAction
	newStatus     string
	acceptsOpen   bool
}

var (
	closeComplete = closeAction{
		name:          "complete",
		txKind:        store.TxComplete,
		historyAction: store.HistoryCompleted,
		newStatus:     "complete",
		acceptsOpen:   true,
	}
	closeFail = closeAction{
		name:          "fail",
		txKind:        store.TxFail,
		historyAction: store.HistoryFailed,
		newStatus:     "failed",
		acceptsOpen:   false,
	}
)

// closeArgs holds the parsed flags. Debrief is required; the pointer
// lets us distinguish unset (nil, exit 2 usage) from empty-string
// (also exit 2 but after state checks).
type closeArgs struct {
	Debrief *string
	PR      *string
}

// parseCloseArgs consumes --debrief and --pr plus an optional leading
// positional ID. @file resolution runs here — missing file /
// oversized file / second @- all exit 2 at parse time before any DB
// I/O.
func parseCloseArgs(action closeAction, cfg config.Config, stdin io.Reader, stderr io.Writer, args []string) (closeArgs, []string, error) {
	_ = cfg
	fs := flag.NewFlagSet(action.name, flag.ContinueOnError)
	fs.SetOutput(stderr)

	var parsed closeArgs
	r := input.NewResolver(stdin)
	fs.Func("debrief", "after-action report (required, supports @file/@-)", func(v string) error {
		resolved, err := r.Resolve("--debrief", v)
		if err != nil {
			return err
		}
		parsed.Debrief = &resolved
		return nil
	})
	fs.Func("pr", "append a PR link (idempotent)", func(v string) error {
		tmp := v
		parsed.PR = &tmp
		return nil
	})

	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return closeArgs{}, nil, nil
		}
		if stderrors.Is(err, errors.ErrUsage) {
			return closeArgs{}, nil, err
		}
		return closeArgs{}, nil, fmt.Errorf("%s: %s: %w", action.name, err.Error(), errors.ErrUsage)
	}
	return parsed, fs.Args(), nil
}

// closeTask is the shared body for `quest complete` and `quest fail`.
// The precondition ladder follows spec §Error precedence: existence
// (3) → ownership (4) → from-status (5) → leaf-direct-close carve-out
// (5, complete-only) → children-terminal (5, parents) → empty-debrief
// usage (2). State checks always precede usage checks so the caller's
// retry logic can switch on exit codes deterministically.
func closeTask(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer, action closeAction) error {
	positional, flagArgs := splitLeadingPositional(args)
	parsed, trailing, err := parseCloseArgs(action, cfg, stdin, stderr, flagArgs)
	if err != nil {
		return err
	}
	positional = append(positional, trailing...)
	id, err := resolveWorkerTaskID(action.name, cfg, positional)
	if err != nil {
		return err
	}
	if parsed.Debrief == nil {
		return fmt.Errorf("%s: --debrief is required: %w", action.name, errors.ErrUsage)
	}

	tx, err := s.BeginImmediate(ctx, action.txKind)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Load the fields the precondition ladder needs plus the telemetry
	// context attributes.
	var (
		status      string
		owner       sql.NullString
		tier, typeV sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT status, owner_session, tier, type FROM tasks WHERE id = ?`, id).
		Scan(&status, &owner, &tier, &typeV)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %q", errors.ErrNotFound, id)
		}
		return fmt.Errorf("%w: %s: %s", errors.ErrGeneral, action.name, err.Error())
	}
	telemetry.RecordTaskContext(ctx, id, tier.String, typeV.String)

	isElevated := config.IsElevated(cfg.Agent.Role, cfg.Workspace.ElevatedRoles)
	if err := store.CheckOwnership(owner.String, cfg.Agent.Session, isElevated); err != nil {
		telemetry.RecordPreconditionFailed(ctx, "ownership", nil)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return err
	}

	// Cancelled is a distinct rejection path — emits the coordination
	// body on stdout so vigil can route the worker to terminate.
	if status == "cancelled" {
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

	// From-status gating.
	openAllowed := action.acceptsOpen
	acceptedAllowed := true
	switch status {
	case "open":
		if !openAllowed {
			telemetry.RecordPreconditionFailed(ctx, "from_status", nil)
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("%s: task is in open status; accept first: %w", action.name, errors.ErrConflict)
		}
	case "accepted":
		if !acceptedAllowed {
			telemetry.RecordPreconditionFailed(ctx, "from_status", nil)
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("%s: invalid from-status %s: %w", action.name, status, errors.ErrConflict)
		}
	default: // complete, failed
		telemetry.RecordPreconditionFailed(ctx, "from_status", nil)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("%s: task is in terminal status (%s): %w", action.name, status, errors.ErrConflict)
	}

	// Leaf-direct-close rejection: complete from open requires at
	// least one child row (direct-close is for parents only). Fires
	// before the children-terminal check so the error body names the
	// carve-out, not the (trivially empty) non-terminal-children list.
	if status == "open" && action.acceptsOpen {
		var anyChild int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE parent = ? LIMIT 1`, id).Scan(&anyChild)
		switch {
		case stderrors.Is(err, sql.ErrNoRows):
			telemetry.RecordPreconditionFailed(ctx, "leaf_direct_close", nil)
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("%s: leaf task cannot be completed from open — accept first: %w", action.name, errors.ErrConflict)
		case err != nil:
			return fmt.Errorf("%w: %s: child probe: %s", errors.ErrGeneral, action.name, err.Error())
		}
	}

	// Children-terminal precondition: applies to parents on every
	// close path (accepted→complete|failed, open→complete).
	rows, err := tx.QueryContext(ctx,
		`SELECT id, status FROM tasks WHERE parent = ? ORDER BY id`, id)
	if err != nil {
		return fmt.Errorf("%w: %s: children query: %s", errors.ErrGeneral, action.name, err.Error())
	}
	var blockers []acceptConflictChild
	var blockerIDs []string
	for rows.Next() {
		var child acceptConflictChild
		if scanErr := rows.Scan(&child.ID, &child.Status); scanErr != nil {
			rows.Close()
			return fmt.Errorf("%w: %s: scan child: %s", errors.ErrGeneral, action.name, scanErr.Error())
		}
		if !terminalStatuses[child.Status] {
			blockers = append(blockers, child)
			blockerIDs = append(blockerIDs, child.ID)
		}
	}
	if rerr := rows.Err(); rerr != nil {
		rows.Close()
		return fmt.Errorf("%w: %s: children iter: %s", errors.ErrGeneral, action.name, rerr.Error())
	}
	rows.Close()
	if len(blockers) > 0 {
		telemetry.RecordPreconditionFailed(ctx, "children_terminal", blockerIDs)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		body := acceptConflictBody{
			Error:               "conflict",
			Task:                id,
			NonTerminalChildren: blockers,
		}
		if emitErr := emitConflictBody(cfg, stdout, body); emitErr != nil {
			return emitErr
		}
		return fmt.Errorf("%s: parent has non-terminal children: %w", action.name, errors.ErrConflict)
	}

	// Usage: literal empty debrief is rejected. Whitespace-only
	// passes per M10 spec decision (plan §Deliberate deviations).
	if *parsed.Debrief == "" {
		return fmt.Errorf("%s: --debrief: empty value rejected: %w", action.name, errors.ErrUsage)
	}

	// Apply the transition: status, completed_at, debrief.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = ?, completed_at = ?, debrief = ? WHERE id = ?`,
		action.newStatus, now, *parsed.Debrief, id); err != nil {
		return fmt.Errorf("%w: %s: update: %s", errors.ErrGeneral, action.name, err.Error())
	}
	if err := store.AppendHistory(ctx, tx, store.History{
		TaskID:    id,
		Timestamp: now,
		Role:      cfg.Agent.Role,
		Session:   cfg.Agent.Session,
		Action:    action.historyAction,
	}); err != nil {
		return err
	}

	// --pr: append if new. History fires only when RowsAffected > 0,
	// matching spec §History field ("Idempotent no-op duplicates ...
	// produce no `pr_added` entry").
	if parsed.PR != nil {
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO prs(task_id, url, added_at) VALUES (?, ?, ?)`,
			id, *parsed.PR, now)
		if err != nil {
			return fmt.Errorf("%w: %s: pr: %s", errors.ErrGeneral, action.name, err.Error())
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			if herr := store.AppendHistory(ctx, tx, store.History{
				TaskID:    id,
				Timestamp: now,
				Role:      cfg.Agent.Role,
				Session:   cfg.Agent.Session,
				Action:    store.HistoryPRAdded,
				Payload:   map[string]any{"url": *parsed.PR},
			}); herr != nil {
				return herr
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	telemetry.RecordStatusTransition(ctx, id, status, action.newStatus)
	telemetry.RecordTerminalState(ctx, id, tier.String, cfg.Agent.Role, action.newStatus)
	return output.Emit(stdout, cfg.Output.Format, closeAck{ID: id, Status: action.newStatus})
}

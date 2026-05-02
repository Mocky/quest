package command

import (
	"context"
	"database/sql"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/input"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// cancelAck mirrors spec §quest cancel. Both arrays are always present;
// empty slices marshal to `[]` rather than `null` because the slice is
// pre-allocated as a zero-length non-nil literal before encoding.
type cancelAck struct {
	Cancelled []string             `json:"cancelled"`
	Skipped   []cancelSkippedEntry `json:"skipped"`
}

// cancelSkippedEntry is one row of the skipped array — a descendant
// that was already in a terminal state (completed/failed/cancelled) at
// the time of the call.
type cancelSkippedEntry struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// cancelArgs captures the two supported flags. Reason is a pointer so
// the handler can distinguish unset from --reason "" (both map to
// `reason: null` in history per spec).
type cancelArgs struct {
	Reason    *string
	Recursive bool
}

func parseCancelArgs(stdin io.Reader, stderr io.Writer, args []string) (cancelArgs, []string, error) {
	fs := newFlagSet("cancel", `ID [--reason "..."] [-r]`,
		"Cancel a task. Transitions status to cancelled. Only available to elevated roles.")
	fs.SetOutput(stderr)

	var parsed cancelArgs
	r := input.NewResolver(stdin)
	fs.Func("reason", "why the task was cancelled (supports @file/@-)", func(v string) error {
		resolved, err := r.Resolve("--reason", v)
		if err != nil {
			return err
		}
		tmp := resolved
		parsed.Reason = &tmp
		return nil
	})
	fs.BoolVar(&parsed.Recursive, "r", false, "recursively cancel all descendants")

	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return cancelArgs{}, nil, err
		}
		if stderrors.Is(err, errors.ErrUsage) {
			return cancelArgs{}, nil, err
		}
		return cancelArgs{}, nil, fmt.Errorf("cancel: %s: %w", err.Error(), errors.ErrUsage)
	}
	return parsed, fs.Args(), nil
}

// Cancel transitions a task (and optionally its descendants) to
// cancelled. Precondition ladder per spec §Error precedence: existence
// (exit 3) → terminal-state (exit 5) → non-terminal children without
// -r (exit 5). An already-cancelled root is idempotent (exit 0 with
// empty arrays and no telemetry side-effects).
func Cancel(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	positional, flagArgs := splitLeadingPositional(args)
	parsed, trailing, err := parseCancelArgs(stdin, stderr, flagArgs)
	if stderrors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	positional = append(positional, trailing...)
	if len(positional) != 1 || positional[0] == "" {
		return fmt.Errorf("cancel: task ID required: %w", errors.ErrUsage)
	}
	id := positional[0]

	kind := store.TxCancel
	if parsed.Recursive {
		kind = store.TxCancelRecursive
	}
	tx, err := s.BeginImmediate(ctx, kind)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		status string
		tier   sql.NullString
		role   sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT status, tier, role FROM tasks WHERE id = ?`, id).
		Scan(&status, &tier, &role)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %q", errors.ErrNotFound, id)
		}
		return fmt.Errorf("%w: cancel: %s", errors.ErrGeneral, err.Error())
	}
	telemetry.RecordTaskContext(ctx, id, tier.String)

	// Terminal-state gating. completed / failed reject; cancelled is
	// idempotent (exit 0 with empty arrays, no telemetry side-effects).
	if status == "completed" || status == "failed" {
		telemetry.RecordPreconditionFailed(ctx, "from_status", nil)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("cancel: task is in terminal status (%s): %w", status, errors.ErrConflict)
	}
	if status == "cancelled" {
		if err := tx.Commit(); err != nil {
			return err
		}
		return emitCancelAck(stdout, cfg.Output.Text, cancelAck{
			Cancelled: []string{},
			Skipped:   []cancelSkippedEntry{},
		})
	}

	// Collect descendants for the -r case, or the non-terminal child
	// probe for the non-recursive case. When the subject's status is
	// open / accepted, both paths need to know the full descendant set.
	descendants, err := loadDescendants(ctx, tx, id)
	if err != nil {
		return err
	}

	if !parsed.Recursive {
		var blockerIDs []string
		for _, d := range descendants {
			if !terminalStatuses[d.status] {
				blockerIDs = append(blockerIDs, d.id)
			}
		}
		if len(blockerIDs) > 0 {
			telemetry.RecordPreconditionFailed(ctx, "children_terminal", blockerIDs)
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("cancel: task has non-terminal descendants (use -r to cancel recursively): %w",
				errors.ErrConflict)
		}
	}

	// --reason "" is equivalent to omitting the flag per spec; history
	// records `reason: null` in both cases.
	reason := ""
	reasonPayload := any(nil)
	if parsed.Reason != nil && *parsed.Reason != "" {
		reason = *parsed.Reason
		reasonPayload = reason
	}

	now := time.Now().UTC().Format(time.RFC3339)
	cancelled := []string{id}
	skipped := []cancelSkippedEntry{}

	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status='cancelled' WHERE id = ?`, id); err != nil {
		return fmt.Errorf("%w: cancel: update: %s", errors.ErrGeneral, err.Error())
	}
	if err := store.AppendHistory(ctx, tx, store.History{
		TaskID:    id,
		Timestamp: now,
		Role:      cfg.Agent.Role,
		Session:   cfg.Agent.Session,
		Action:    store.HistoryCancelled,
		Payload:   map[string]any{"reason": reasonPayload},
	}); err != nil {
		return err
	}

	// Descendants: with -r, transition non-terminal ones and collect
	// skipped (already-terminal) ones. Without -r, the blockerIDs
	// check above already failed, so we only reach here with empty
	// descendants or all-terminal descendants.
	type transitioned struct {
		id       string
		fromStat string
		tier     string
		role     string
	}
	var transitions []transitioned
	for _, d := range descendants {
		if terminalStatuses[d.status] {
			if parsed.Recursive {
				skipped = append(skipped, cancelSkippedEntry{ID: d.id, Status: d.status})
			}
			continue
		}
		if !parsed.Recursive {
			// Defensive: non-recursive should have failed above.
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status='cancelled' WHERE id = ?`, d.id); err != nil {
			return fmt.Errorf("%w: cancel: descendant update: %s", errors.ErrGeneral, err.Error())
		}
		if err := store.AppendHistory(ctx, tx, store.History{
			TaskID:    d.id,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    store.HistoryCancelled,
			Payload:   map[string]any{"reason": reasonPayload},
		}); err != nil {
			return err
		}
		cancelled = append(cancelled, d.id)
		transitions = append(transitions, transitioned{
			id:       d.id,
			fromStat: d.status,
			tier:     d.tier,
			role:     d.role,
		})
	}

	// Stable order per spec: target first, then descendants by ID.
	sort.Strings(cancelled[1:])
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ID < skipped[j].ID })

	if err := tx.Commit(); err != nil {
		return err
	}

	// Post-commit telemetry.
	telemetry.RecordCancelOutcome(ctx, id, parsed.Recursive, len(cancelled), len(skipped))
	telemetry.RecordStatusTransition(ctx, id, status, "cancelled")
	telemetry.RecordTerminalState(ctx, id, tier.String, role.String, "cancelled")
	for _, t := range transitions {
		telemetry.RecordStatusTransition(ctx, t.id, t.fromStat, "cancelled")
		telemetry.RecordTerminalState(ctx, t.id, t.tier, t.role, "cancelled")
	}
	if parsed.Reason != nil && *parsed.Reason != "" && telemetry.CaptureContentEnabled() {
		telemetry.RecordContentReason(ctx, reason)
	}

	return emitCancelAck(stdout, cfg.Output.Text, cancelAck{
		Cancelled: cancelled,
		Skipped:   skipped,
	})
}

// descendantRow is the per-row shape loadDescendants returns; tier and
// role feed RecordTerminalState for each transitioned descendant.
type descendantRow struct {
	id     string
	status string
	tier   string
	role   string
}

// loadDescendants walks the subgraph rooted at id breadth-first and
// returns every descendant's id/status/tier/role. The cancel handler
// uses the result for both the non-recursive blocker probe (blocker =
// non-terminal descendant) and the recursive transition pass.
func loadDescendants(ctx context.Context, tx *store.Tx, id string) ([]descendantRow, error) {
	var out []descendantRow
	queue := []string{id}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		rows, err := tx.QueryContext(ctx,
			`SELECT id, status, tier, role FROM tasks WHERE parent = ? ORDER BY id`, parent)
		if err != nil {
			return nil, fmt.Errorf("%w: cancel: load descendants: %s", errors.ErrGeneral, err.Error())
		}
		var batch []descendantRow
		for rows.Next() {
			var r descendantRow
			var tier, role sql.NullString
			if err := rows.Scan(&r.id, &r.status, &tier, &role); err != nil {
				rows.Close()
				return nil, fmt.Errorf("%w: cancel: scan descendant: %s", errors.ErrGeneral, err.Error())
			}
			r.tier = tier.String
			r.role = role.String
			batch = append(batch, r)
		}
		if rerr := rows.Err(); rerr != nil {
			rows.Close()
			return nil, fmt.Errorf("%w: cancel: descendant iter: %s", errors.ErrGeneral, rerr.Error())
		}
		rows.Close()
		for _, r := range batch {
			out = append(out, r)
			queue = append(queue, r.id)
		}
	}
	return out, nil
}

// emitCancelAck renders the ack in the active mode. Text mode
// matches spec §quest cancel: one `cancelled: <id>` line per cancelled
// task, then one `skipped: <id> (<status>)` line per skipped entry.
func emitCancelAck(stdout io.Writer, text bool, ack cancelAck) error {
	if text {
		for _, id := range ack.Cancelled {
			if _, err := fmt.Fprintf(stdout, "cancelled: %s\n", id); err != nil {
				return err
			}
		}
		for _, s := range ack.Skipped {
			if _, err := fmt.Fprintf(stdout, "skipped: %s (%s)\n", s.ID, s.Status); err != nil {
				return err
			}
		}
		return nil
	}
	return output.Emit(stdout, text, ack)
}

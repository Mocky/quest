package command

import (
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// acceptAck is the spec §Write-command output shapes success body.
// Fields are always present; status is the literal string "accepted".
type acceptAck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// acceptConflictChild is one row of the non_terminal_children array in
// the structured conflict body emitted when accepting a parent whose
// children are still in flight.
type acceptConflictChild struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// acceptConflictBody mirrors the spec-pinned shape in §quest accept
// ("Output shape for this conflict"). Agents switch on
// `non_terminal_children` to extract the blocking IDs.
type acceptConflictBody struct {
	Error               string                `json:"error"`
	Task                string                `json:"task"`
	NonTerminalChildren []acceptConflictChild `json:"non_terminal_children"`
}

// terminalStatuses are the states that satisfy the parent-accept
// precondition. A child in any other status blocks the parent transition.
var terminalStatuses = map[string]bool{
	"completed": true,
	"failed":    true,
	"cancelled": true,
}

// acceptFlagSet returns the unparsed FlagSet shared by the Accept
// handler and the help dispatcher. accept takes only a positional ID;
// the FlagSet has no flags but is still the source of synopsis +
// description for help rendering.
func acceptFlagSet() *flag.FlagSet {
	return newFlagSet("accept", "ID",
		"Signal that the agent has received the task and begun work. Transitions status from open to accepted.")
}

// AcceptHelp is the descriptor-side help builder.
func AcceptHelp() *flag.FlagSet { return acceptFlagSet() }

// Accept transitions a task from open to accepted. Leaves and parents
// follow the same BEGIN IMMEDIATE path (spec §quest accept) so the
// existence-vs-status distinction (exit 3 vs exit 5) is visible to the
// caller. A parent with any non-terminal child is rejected with a
// structured stdout body plus the standard stderr two-liner; a
// non-open leaf/parent is rejected with exit 5 and an empty stdout —
// the vigil-coordination cancelled body is scoped to update/complete/
// fail, not accept.
func Accept(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	positional, flagArgs := splitLeadingPositional(args)
	// FlagSet has no flags of its own — accept takes only the positional
	// ID — but the parse step rejects flag-shaped residue (e.g. `--foo`)
	// as a usage error before positional validation.
	fs := acceptFlagSet()
	fs.SetOutput(stderr)
	if err := fs.Parse(flagArgs); err != nil {
		return fmt.Errorf("accept: %s: %w", err.Error(), errors.ErrUsage)
	}
	positional = append(positional, fs.Args()...)
	id, err := resolveWorkerTaskID("accept", positional)
	if err != nil {
		return err
	}

	tx, err := s.BeginImmediate(ctx, store.TxAccept)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		status string
		tier   sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT status, tier FROM tasks WHERE id = ?`, id).
		Scan(&status, &tier)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %q", errors.ErrNotFound, id)
		}
		return fmt.Errorf("%w: accept: %s", errors.ErrGeneral, err.Error())
	}
	telemetry.RecordTaskContext(ctx, id, tier.String)

	if status != "open" {
		telemetry.RecordPreconditionFailed(ctx, "from_status", nil)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("task is not in open status (current: %s): %w", status, errors.ErrConflict)
	}

	// Parent check: non-terminal children block the transition. Leaves
	// skip this branch entirely because the inner SELECT returns zero
	// rows. The plain SELECT scans every child status so the error body
	// can name each blocker.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, status FROM tasks WHERE parent = ? ORDER BY id`, id)
	if err != nil {
		return fmt.Errorf("%w: accept: children query: %s", errors.ErrGeneral, err.Error())
	}
	var blockers []acceptConflictChild
	var blockerIDs []string
	for rows.Next() {
		var child acceptConflictChild
		if scanErr := rows.Scan(&child.ID, &child.Status); scanErr != nil {
			rows.Close()
			return fmt.Errorf("%w: accept: scan child: %s", errors.ErrGeneral, scanErr.Error())
		}
		if !terminalStatuses[child.Status] {
			blockers = append(blockers, child)
			blockerIDs = append(blockerIDs, child.ID)
		}
	}
	if rerr := rows.Err(); rerr != nil {
		rows.Close()
		return fmt.Errorf("%w: accept: children iter: %s", errors.ErrGeneral, rerr.Error())
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
		return fmt.Errorf("parent has non-terminal children: %w", errors.ErrConflict)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	ownerSess := cfg.Agent.Session
	ownerArg := any(sql.NullString{})
	if ownerSess != "" {
		ownerArg = ownerSess
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status='accepted', owner_session=?, started_at=? WHERE id=?`,
		ownerArg, now, id); err != nil {
		return fmt.Errorf("%w: accept: update: %s", errors.ErrGeneral, err.Error())
	}
	if err := store.AppendHistory(ctx, tx, store.History{
		TaskID:    id,
		Timestamp: now,
		Role:      cfg.Agent.Role,
		Session:   cfg.Agent.Session,
		Action:    store.HistoryAccepted,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	telemetry.RecordStatusTransition(ctx, id, "open", "accepted")
	return output.Emit(stdout, cfg.Output.Text, acceptAck{ID: id, Status: "accepted"})
}

// emitConflictBody writes a conflict struct to stdout in the active
// output mode. JSON mode uses the standard encoder; text mode falls
// back to a one-liner summarizing the blocking IDs so human-facing
// callers still get something readable (the JSON body is the contract).
func emitConflictBody(cfg config.Config, stdout io.Writer, body acceptConflictBody) error {
	if cfg.Output.Text {
		_, err := fmt.Fprintf(stdout, "conflict: %s has non-terminal children: %s\n",
			body.Task, formatConflictChildren(body.NonTerminalChildren))
		return err
	}
	enc := json.NewEncoder(stdout)
	return enc.Encode(body)
}

// formatConflictChildren is a text-mode helper — JSON mode bypasses it
// entirely. Format is stable-ish but not a contract; agents parse JSON.
func formatConflictChildren(children []acceptConflictChild) string {
	if len(children) == 0 {
		return ""
	}
	out := ""
	for i, c := range children {
		if i > 0 {
			out += ", "
		}
		out += c.ID + "=" + c.Status
	}
	return out
}

// resolveWorkerTaskID extracts the required task ID from args[0] for
// every worker handler (show, accept, update, complete, fail). The ID
// is a required positional argument — worker commands do not fall back
// to AGENT_TASK (that env var is identity/telemetry metadata, not a
// CLI convenience; see spec §Role Gating > Resolution logic).
func resolveWorkerTaskID(command string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%s: unexpected positional arguments: %w", command, errors.ErrUsage)
	}
	if len(args) == 1 && args[0] != "" {
		return args[0], nil
	}
	return "", fmt.Errorf("%s: task ID is required: %w", command, errors.ErrUsage)
}

// splitLeadingPositional separates a leading positional argument (the
// task ID, per the `quest <cmd> [ID] [flags]` convention) from the
// remaining flag args. Go's stdlib flag package stops at the first
// non-flag token, so the conventional "ID first, then flags" CLI
// order requires this manual split before flag.Parse sees the flags.
// Returns positional (zero or one element) and the rest to pass to
// flag.Parse.
func splitLeadingPositional(args []string) (positional []string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return []string{args[0]}, args[1:]
	}
	return nil, args
}

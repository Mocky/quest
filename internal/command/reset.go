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

// resetAck is the spec §Write-command output shapes success body —
// {"id": "<id>", "status": "open"}. Status is the literal "open" on
// success; both fields always present.
type resetAck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type resetArgs struct {
	Reason *string
}

func parseResetArgs(stdin io.Reader, stderr io.Writer, args []string) (resetArgs, []string, error) {
	fs := newFlagSet("reset")
	fs.SetOutput(stderr)

	var parsed resetArgs
	r := input.NewResolver(stdin)
	fs.Func("reason", "why the task is being reset (supports @file/@-)", func(v string) error {
		resolved, err := r.Resolve("--reason", v)
		if err != nil {
			return err
		}
		tmp := resolved
		parsed.Reason = &tmp
		return nil
	})

	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return resetArgs{}, nil, err
		}
		if stderrors.Is(err, errors.ErrUsage) {
			return resetArgs{}, nil, err
		}
		return resetArgs{}, nil, fmt.Errorf("reset: %s: %w", err.Error(), errors.ErrUsage)
	}
	return parsed, fs.Args(), nil
}

// Reset moves an accepted task back to open, clearing owner_session and
// started_at while preserving handoff / notes for the next session.
// Precondition ladder per spec §Error precedence: existence (exit 3)
// then from-status (exit 5) — accepted is the only legal source.
func Reset(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	positional, flagArgs := splitLeadingPositional(args)
	parsed, trailing, err := parseResetArgs(stdin, stderr, flagArgs)
	if stderrors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	positional = append(positional, trailing...)
	if len(positional) != 1 || positional[0] == "" {
		return fmt.Errorf("reset: task ID required: %w", errors.ErrUsage)
	}
	id := positional[0]

	tx, err := s.BeginImmediate(ctx, store.TxReset)
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
		return fmt.Errorf("%w: reset: %s", errors.ErrGeneral, err.Error())
	}
	telemetry.RecordTaskContext(ctx, id, tier.String)

	if status != "accepted" {
		telemetry.RecordPreconditionFailed(ctx, "from_status", nil)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("reset: task is not in accepted status (current: %s): %w", status, errors.ErrConflict)
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
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status='open', owner_session=NULL, started_at=NULL WHERE id = ?`, id); err != nil {
		return fmt.Errorf("%w: reset: update: %s", errors.ErrGeneral, err.Error())
	}
	if err := store.AppendHistory(ctx, tx, store.History{
		TaskID:    id,
		Timestamp: now,
		Role:      cfg.Agent.Role,
		Session:   cfg.Agent.Session,
		Action:    store.HistoryReset,
		Payload:   map[string]any{"reason": reasonPayload},
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	telemetry.RecordStatusTransition(ctx, id, "accepted", "open")
	if parsed.Reason != nil && *parsed.Reason != "" && telemetry.CaptureContentEnabled() {
		telemetry.RecordContentReason(ctx, reason)
	}
	return emitResetAck(stdout, cfg.Output.Text, resetAck{ID: id, Status: "open"})
}

func emitResetAck(stdout io.Writer, text bool, ack resetAck) error {
	if text {
		_, err := fmt.Fprintf(stdout, "%s reset to open\n", ack.ID)
		return err
	}
	return output.Emit(stdout, text, ack)
}

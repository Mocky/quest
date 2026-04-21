package command

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"io"
	"time"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// Unlink removes a typed dependency edge from TASK to TARGET. Removing
// a non-existent edge is idempotent: DELETE plus a RowsAffected check
// skip the history append + telemetry recorder when no row was removed.
func Unlink(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	taskID, rest, err := resolveLinkPositional("unlink", args)
	if err != nil {
		return err
	}
	parsed, trailing, err := parseLinkArgs(stderr, "unlink", rest)
	if err != nil {
		return err
	}
	if len(trailing) > 0 {
		return fmt.Errorf("unlink: unexpected positional arguments: %w", errors.ErrUsage)
	}
	edge, err := validateLinkArgs("unlink", parsed)
	if err != nil {
		return err
	}

	tx, err := s.BeginImmediate(ctx, store.TxUnlink)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		taskType, tier sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT type, tier FROM tasks WHERE id = ?`, taskID).
		Scan(&taskType, &tier)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %q", errors.ErrNotFound, taskID)
		}
		return fmt.Errorf("%w: unlink: %s", errors.ErrGeneral, err.Error())
	}
	telemetry.RecordTaskContext(ctx, taskID, tier.String, taskType.String)

	res, err := tx.ExecContext(ctx,
		`DELETE FROM dependencies WHERE task_id = ? AND target_id = ? AND link_type = ?`,
		taskID, edge.Target, edge.LinkType)
	if err != nil {
		return fmt.Errorf("%w: unlink: delete: %s", errors.ErrGeneral, err.Error())
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := store.AppendHistory(ctx, tx, store.History{
			TaskID:    taskID,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    store.HistoryUnlinked,
			Payload: map[string]any{
				"target":    edge.Target,
				"link_type": edge.LinkType,
			},
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if rows > 0 {
		telemetry.RecordLinkRemoved(ctx, taskID, edge.Target, edge.LinkType)
	}
	return output.Emit(stdout, cfg.Output.Text, linkAck{
		Task:     taskID,
		Target:   edge.Target,
		LinkType: edge.LinkType,
	})
}

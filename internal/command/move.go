package command

import (
	"context"
	"database/sql"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/ids"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// moveAck is the spec §quest move output shape. `id` is the new ID of
// the moved root; `renames` is the full old→new map ordered by old ID.
// Both fields always present; `renames` has at least one entry (the
// moved task itself) on success.
type moveAck struct {
	ID      string       `json:"id"`
	Renames []moveRename `json:"renames"`
}

// moveRename is a single {old, new} row in the renames array. Keys are
// lowercase per spec example; preserving the spec names keeps the
// contract intact.
type moveRename struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type moveArgs struct {
	Parent *string
}

func parseMoveArgs(stderr io.Writer, args []string) (moveArgs, []string, error) {
	fs := newFlagSet("move", "ID --parent NEW_PARENT",
		"Reparent a task under a different parent. Only available to elevated roles.")
	fs.SetOutput(stderr)

	var parsed moveArgs
	fs.Func("parent", "new parent task ID (required)", func(v string) error {
		tmp := v
		parsed.Parent = &tmp
		return nil
	})

	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return moveArgs{}, nil, err
		}
		if stderrors.Is(err, errors.ErrUsage) {
			return moveArgs{}, nil, err
		}
		return moveArgs{}, nil, fmt.Errorf("move: %s: %w", err.Error(), errors.ErrUsage)
	}
	return parsed, fs.Args(), nil
}

// Move reparents a task under NEW_PARENT, renaming the moved task and
// every descendant. Scoped to the planning-and-verification window per
// spec §quest move — once any task in the subgraph has been accepted,
// the rename is refused. ON UPDATE CASCADE propagates the new IDs to
// every side table (history, dependencies, tags, prs, notes,
// subtask_counter) inside the same transaction.
func Move(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	positional, flagArgs := splitLeadingPositional(args)
	parsed, trailing, err := parseMoveArgs(stderr, flagArgs)
	if stderrors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	positional = append(positional, trailing...)
	if len(positional) != 1 || positional[0] == "" {
		return fmt.Errorf("move: task ID required: %w", errors.ErrUsage)
	}
	if parsed.Parent == nil {
		return fmt.Errorf("move: --parent is required: %w", errors.ErrUsage)
	}
	if *parsed.Parent == "" {
		return fmt.Errorf("move: --parent: empty value rejected: %w", errors.ErrUsage)
	}
	oldID := positional[0]
	newParentID := *parsed.Parent

	if oldID == newParentID {
		telemetry.RecordPreconditionFailed(ctx, "cycle", []string{oldID})
		return fmt.Errorf("move: task cannot be its own parent: %w", errors.ErrConflict)
	}

	tx, err := s.BeginImmediate(ctx, store.TxMove)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Load the moved task: existence + current parent + task-context
	// attributes for telemetry.
	var (
		oldParent sql.NullString
		tier      sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT parent, tier FROM tasks WHERE id = ?`, oldID).
		Scan(&oldParent, &tier)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %q", errors.ErrNotFound, oldID)
		}
		return fmt.Errorf("%w: move: %s", errors.ErrGeneral, err.Error())
	}

	// Load NEW_PARENT: existence + status.
	var newParentStatus string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM tasks WHERE id = ?`, newParentID).
		Scan(&newParentStatus)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: new parent %q", errors.ErrNotFound, newParentID)
		}
		return fmt.Errorf("%w: move: new parent lookup: %s", errors.ErrGeneral, err.Error())
	}

	// Build the moved subgraph (root + descendants by BFS). Depth is
	// computed from the id shape so we don't need to thread it through
	// the walk.
	subgraph, err := loadSubgraphIDs(ctx, tx, oldID)
	if err != nil {
		return err
	}

	// Circular parentage: NEW_PARENT must not be in the moved subgraph.
	for _, id := range subgraph {
		if id == newParentID {
			telemetry.RecordPreconditionFailed(ctx, "cycle", []string{oldID, newParentID})
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("move: circular parentage: %q is within the moved subgraph: %w",
				newParentID, errors.ErrConflict)
		}
	}

	// NEW_PARENT must be open.
	if newParentStatus != "open" {
		telemetry.RecordPreconditionFailed(ctx, "parent_not_open", []string{newParentID})
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("move: new parent %q is not in open status (current: %s): %w",
			newParentID, newParentStatus, errors.ErrConflict)
	}

	// Moved task's current parent must not be in accepted status.
	if oldParent.Valid && oldParent.String != "" {
		var curParentStatus string
		if err := tx.QueryRowContext(ctx,
			`SELECT status FROM tasks WHERE id = ?`, oldParent.String).Scan(&curParentStatus); err != nil {
			return fmt.Errorf("%w: move: current parent lookup: %s", errors.ErrGeneral, err.Error())
		}
		if curParentStatus == "accepted" {
			telemetry.RecordPreconditionFailed(ctx, "move_parent_accepted", []string{oldParent.String})
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return fmt.Errorf("move: current parent %q is in accepted status: %w",
				oldParent.String, errors.ErrConflict)
		}
	}

	// No task in the subgraph may have an `accepted` action anywhere
	// in its history. The check is on history, not current status —
	// a task that was accepted and then reset back to open still
	// blocks the move.
	acceptedIDs, err := acceptedInHistory(ctx, tx, subgraph)
	if err != nil {
		return err
	}
	if len(acceptedIDs) > 0 {
		telemetry.RecordPreconditionFailed(ctx, "move_history_accepted", acceptedIDs)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("move: subgraph contains tasks with accepted history: %s: %w",
			strings.Join(acceptedIDs, ", "), errors.ErrConflict)
	}

	// Depth: compute newID prefix shift and validate the deepest
	// descendant does not exceed MaxDepth. We need NEW_PARENT's depth
	// to allocate the new root ID; depth check runs before the counter
	// increment so a failed move does not consume an ID.
	newRootDepth := ids.Depth(newParentID) + 1
	oldRootDepth := ids.Depth(oldID)
	depthShift := newRootDepth - oldRootDepth
	var deepest string
	deepestDepth := 0
	for _, id := range subgraph {
		if d := ids.Depth(id) + depthShift; d > deepestDepth {
			deepest = id
			deepestDepth = d
		}
	}
	if deepestDepth > ids.MaxDepth {
		telemetry.RecordPreconditionFailed(ctx, "depth_exceeded", []string{deepest})
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("move: would exceed max depth %d (task %q would be at depth %d): %w",
			ids.MaxDepth, deepest, deepestDepth, errors.ErrConflict)
	}

	// Pre-rename cascade count. RowsAffected on the UPDATE tasks pass
	// does not see FK cascade side-effects (per plan note), so count
	// the dependencies rows the cascade will rewrite before we run it.
	var depUpdates int
	depUpdates, err = countDependencyCascade(ctx, tx, subgraph)
	if err != nil {
		return err
	}

	// Allocate the new root ID under NEW_PARENT. This consumes one
	// slot from subtask_counter[NEW_PARENT]; the cascade on the
	// rename pass below rewrites subtask_counter[oldID] →
	// subtask_counter[newRootID] so descendants can still allocate
	// cleanly if further create / batch calls follow.
	newRootID, err := ids.NewSubTask(ctx, tx, newParentID)
	if err != nil {
		return err
	}

	// Rename pass. Sort by depth ascending so the root renames
	// first, then each level under it. Descendants inherit cascaded
	// parent updates from the step above; the explicit parent column
	// in the UPDATE is redundant with the cascade but keeps the
	// statement self-consistent.
	ctx2, end := telemetry.StoreSpan(ctx, "quest.store.rename_subgraph")
	var cascadeErr error
	renames := make([]moveRename, 0, len(subgraph))
	defer func() { end(cascadeErr) }()

	sort.SliceStable(subgraph, func(i, j int) bool {
		return ids.Depth(subgraph[i]) < ids.Depth(subgraph[j])
	})

	now := time.Now().UTC().Format(time.RFC3339)
	for _, oldSubID := range subgraph {
		newSubID := rewriteSubgraphID(oldSubID, oldID, newRootID)
		var newParentCol any
		if oldSubID == oldID {
			newParentCol = newParentID
		} else {
			newParentCol = ids.Parent(newSubID)
		}
		if _, cerr := tx.ExecContext(ctx2,
			`UPDATE tasks SET id = ?, parent = ? WHERE id = ?`,
			newSubID, newParentCol, oldSubID); cerr != nil {
			cascadeErr = cerr
			return fmt.Errorf("%w: move: rename %q → %q: %s",
				errors.ErrGeneral, oldSubID, newSubID, cerr.Error())
		}
		if herr := store.AppendHistory(ctx2, tx, store.History{
			TaskID:    newSubID,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    store.HistoryMoved,
			Payload:   map[string]any{"old_id": oldSubID, "new_id": newSubID},
		}); herr != nil {
			cascadeErr = herr
			return herr
		}
		renames = append(renames, moveRename{Old: oldSubID, New: newSubID})
	}

	// Spec orders renames by old ID ascending.
	sort.Slice(renames, func(i, j int) bool { return renames[i].Old < renames[j].Old })

	if err := tx.Commit(); err != nil {
		cascadeErr = err
		return err
	}

	// Post-commit telemetry uses the post-rename root ID so
	// retrospective queries keyed on quest.task.id find the move.
	telemetry.RecordTaskContext(ctx, newRootID, tier.String)
	telemetry.RecordMoveOutcome(ctx, oldID, newRootID, len(renames), depUpdates)

	return emitMoveAck(stdout, cfg.Output.Text, moveAck{
		ID:      newRootID,
		Renames: renames,
	})
}

// loadSubgraphIDs returns the moved task ID plus every descendant by
// BFS order. Used for the circular-parentage check, the accepted-in-
// history check, the depth check, and the rename pass.
func loadSubgraphIDs(ctx context.Context, tx *store.Tx, rootID string) ([]string, error) {
	collected := []string{rootID}
	queue := []string{rootID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM tasks WHERE parent = ? ORDER BY id`, parent)
		if err != nil {
			return nil, fmt.Errorf("%w: move: load subgraph: %s", errors.ErrGeneral, err.Error())
		}
		var batch []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("%w: move: scan subgraph: %s", errors.ErrGeneral, err.Error())
			}
			batch = append(batch, id)
		}
		if rerr := rows.Err(); rerr != nil {
			rows.Close()
			return nil, fmt.Errorf("%w: move: subgraph iter: %s", errors.ErrGeneral, rerr.Error())
		}
		rows.Close()
		for _, id := range batch {
			collected = append(collected, id)
			queue = append(queue, id)
		}
	}
	return collected, nil
}

// acceptedInHistory returns the sorted subset of subgraph IDs that have
// an `accepted` row anywhere in history — the spec's "has an accepted
// action anywhere in its history" check.
func acceptedInHistory(ctx context.Context, tx *store.Tx, subgraph []string) ([]string, error) {
	if len(subgraph) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(subgraph))
	placeholders = placeholders[:len(placeholders)-1]
	q := `SELECT DISTINCT task_id FROM history WHERE action='accepted' AND task_id IN (` + placeholders + `) ORDER BY task_id`
	argv := make([]any, 0, len(subgraph))
	for _, id := range subgraph {
		argv = append(argv, id)
	}
	rows, err := tx.QueryContext(ctx, q, argv...)
	if err != nil {
		return nil, fmt.Errorf("%w: move: history probe: %s", errors.ErrGeneral, err.Error())
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%w: move: scan history: %s", errors.ErrGeneral, err.Error())
		}
		out = append(out, id)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("%w: move: history iter: %s", errors.ErrGeneral, rerr.Error())
	}
	return out, nil
}

// countDependencyCascade returns the number of dependency rows the FK
// ON UPDATE CASCADE will rewrite when the subgraph renames. Plan note:
// sql.Result.RowsAffected on the UPDATE tasks pass does not include
// cascade side-effects, so we count ahead of time.
func countDependencyCascade(ctx context.Context, tx *store.Tx, subgraph []string) (int, error) {
	if len(subgraph) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(subgraph))
	placeholders = placeholders[:len(placeholders)-1]
	q := `SELECT COUNT(*) FROM dependencies
	       WHERE task_id IN (` + placeholders + `) OR target_id IN (` + placeholders + `)`
	argv := make([]any, 0, len(subgraph)*2)
	for _, id := range subgraph {
		argv = append(argv, id)
	}
	for _, id := range subgraph {
		argv = append(argv, id)
	}
	var n int
	if err := tx.QueryRowContext(ctx, q, argv...).Scan(&n); err != nil {
		return 0, fmt.Errorf("%w: move: dep count: %s", errors.ErrGeneral, err.Error())
	}
	return n, nil
}

// rewriteSubgraphID swaps the oldRootID prefix for newRootID in id.
// For the root itself returns newRootID directly; for descendants the
// suffix after oldRootID (including the leading `.`) is preserved.
func rewriteSubgraphID(id, oldRootID, newRootID string) string {
	if id == oldRootID {
		return newRootID
	}
	return newRootID + strings.TrimPrefix(id, oldRootID)
}

func emitMoveAck(stdout io.Writer, text bool, ack moveAck) error {
	if text {
		for _, r := range ack.Renames {
			if _, err := fmt.Fprintf(stdout, "%s → %s\n", r.Old, r.New); err != nil {
				return err
			}
		}
		return nil
	}
	return output.Emit(stdout, text, ack)
}

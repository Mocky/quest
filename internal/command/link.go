package command

import (
	"context"
	"database/sql"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// linkAck is the spec §Write-command output shape for `link` and
// `unlink`: the edge identified by source task, target task, and
// link type. Same shape applies on idempotent no-ops — callers cannot
// distinguish "added now" from "already present" from the body. The
// relationship primitive key is `link_type` to match the field name
// used everywhere else a link type appears (show dependencies, graph
// edges, batch input).
type linkAck struct {
	Task     string `json:"task"`
	Target   string `json:"target"`
	LinkType string `json:"link_type"`
}

// linkArgs captures the four mutually-exclusive relationship flags as
// (target, link-type) pairs in the order they were parsed. Each flag
// carries the target ID; the handler enforces "exactly one relationship
// flag per invocation" before any DB work.
type linkArgs struct {
	edges []batch.Edge
}

func parseLinkArgs(stderr io.Writer, name string, args []string) (linkArgs, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)

	var parsed linkArgs
	add := func(linkType string) func(string) error {
		return func(v string) error {
			parsed.edges = append(parsed.edges, batch.Edge{Target: v, LinkType: linkType})
			return nil
		}
	}
	fs.Func("blocked-by", "TARGET (default link type)", add(batch.LinkBlockedBy))
	fs.Func("caused-by", "TARGET", add(batch.LinkCausedBy))
	fs.Func("discovered-from", "TARGET", add(batch.LinkDiscoveredFrom))
	fs.Func("retry-of", "TARGET", add(batch.LinkRetryOf))

	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return linkArgs{}, nil, nil
		}
		if stderrors.Is(err, errors.ErrUsage) {
			return linkArgs{}, nil, err
		}
		return linkArgs{}, nil, fmt.Errorf("%s: %s: %w", name, err.Error(), errors.ErrUsage)
	}
	return parsed, fs.Args(), nil
}

// resolveLinkPositional unifies the leading positional parsing for
// `link` and `unlink`. Returns (taskID, rest, error); any further
// positionals stay in rest and are surfaced as "unexpected positional
// arguments" once flag parsing runs.
func resolveLinkPositional(name string, args []string) (string, []string, error) {
	leading, rest := splitLeadingPositional(args)
	if len(leading) == 0 {
		return "", nil, fmt.Errorf("%s: task ID required: %w", name, errors.ErrUsage)
	}
	taskID := leading[0]
	if taskID == "" {
		return "", nil, fmt.Errorf("%s: task ID required: %w", name, errors.ErrUsage)
	}
	return taskID, rest, nil
}

// validateLinkArgs picks the single edge from parsed flags and rejects
// ambiguous invocations (two relationship flags, missing target).
func validateLinkArgs(name string, parsed linkArgs) (batch.Edge, error) {
	if len(parsed.edges) > 1 {
		return batch.Edge{}, fmt.Errorf(
			"%s: at most one relationship flag may be passed: %w", name, errors.ErrUsage)
	}
	if len(parsed.edges) == 1 {
		e := parsed.edges[0]
		if e.Target == "" {
			return batch.Edge{}, fmt.Errorf(
				"%s: --%s: empty target rejected: %w", name, e.LinkType, errors.ErrUsage)
		}
		return e, nil
	}
	return batch.Edge{}, fmt.Errorf("%s: TARGET required: %w", name, errors.ErrUsage)
}

// Link adds a typed dependency edge from TASK to TARGET. Duplicate
// (task, target, type) triples are idempotent: INSERT OR IGNORE plus a
// RowsAffected check skip the history append + telemetry recorder when
// the edge was already present.
func Link(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	taskID, rest, err := resolveLinkPositional("link", args)
	if err != nil {
		return err
	}
	parsed, trailing, err := parseLinkArgs(stderr, "link", rest)
	if err != nil {
		return err
	}
	if len(trailing) > 0 {
		return fmt.Errorf("link: unexpected positional arguments: %w", errors.ErrUsage)
	}
	edge, err := validateLinkArgs("link", parsed)
	if err != nil {
		return err
	}

	tx, err := s.BeginImmediate(ctx, store.TxLink)
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
		return fmt.Errorf("%w: link: %s", errors.ErrGeneral, err.Error())
	}
	telemetry.RecordTaskContext(ctx, taskID, tier.String, taskType.String)

	// Self-reference cycle check runs inside the tx *after* source
	// existence so a `quest link ghost --blocked-by ghost` against a
	// missing task returns exit 3 (not_found) rather than exit 5, per
	// spec §Error precedence (existence beats cycle). The dedicated
	// block preserves the RecordCycleDetected event shape
	// ([taskID, taskID]) that the generic ValidateSemantic cycle path
	// would not emit identically.
	if taskID == edge.Target && edge.LinkType == batch.LinkBlockedBy {
		telemetry.RecordCycleDetected(ctx, []string{taskID, edge.Target})
		telemetry.RecordPreconditionFailed(ctx, "cycle", []string{taskID})
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("link: task cannot be blocked-by itself: %w", errors.ErrConflict)
	}

	// Dependency validation (existence of target, semantic constraints,
	// cycle detection on blocked-by). The validator reads against the
	// committed graph; any new edge from this tx is included via the
	// inFlight slice.
	depErrs := batch.ValidateSemantic(ctx, s, batch.TaskShape{
		ID:   taskID,
		Type: taskType.String,
	}, []batch.Edge{edge})
	if len(depErrs) > 0 {
		// Map first dep error to its precondition. Cycles emit the path
		// event before the precondition_failed event per OTEL.md §13.4.
		precondition := "existence"
		var blockedBy []string
		for _, de := range depErrs {
			switch de.Code {
			case batch.CodeCycle:
				telemetry.RecordCycleDetected(ctx, de.Path)
				precondition = "cycle"
				blockedBy = de.Path
			case batch.CodeUnknownTaskID:
				telemetry.RecordPreconditionFailed(ctx, "existence", []string{de.Target})
				tx.MarkOutcome(store.TxRolledBackPrecondition)
				return fmt.Errorf("%w: target task %q", errors.ErrNotFound, de.Target)
			case batch.CodeBlockedByCancelled:
				precondition = "from_status"
				blockedBy = []string{de.Target}
			case batch.CodeRetryTargetStatus:
				precondition = "from_status"
				blockedBy = []string{de.Target}
			case batch.CodeSourceTypeRequired:
				precondition = "type_transition"
			}
		}
		telemetry.RecordPreconditionFailed(ctx, precondition, blockedBy)
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return formatDepErrors(depErrs)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO dependencies(task_id, target_id, link_type, created_at)
		 VALUES (?, ?, ?, ?)`,
		taskID, edge.Target, edge.LinkType, now)
	if err != nil {
		return fmt.Errorf("%w: link: insert: %s", errors.ErrGeneral, err.Error())
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		if err := store.AppendHistory(ctx, tx, store.History{
			TaskID:    taskID,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    store.HistoryLinked,
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
		telemetry.RecordLinkAdded(ctx, taskID, edge.Target, edge.LinkType)
	}
	return output.Emit(stdout, cfg.Output.Format, linkAck{
		Task:     taskID,
		Target:   edge.Target,
		LinkType: edge.LinkType,
	})
}

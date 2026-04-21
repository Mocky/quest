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

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// tagAck is the spec §Write-command output shape for `tag` and `untag`:
// the task ID plus the full post-state tag list, sorted lowercase. Same
// shape on idempotent no-ops (unchanged post-state list).
type tagAck struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
}

// resolveTagPositional parses the leading positional arguments shared
// by `tag` and `untag`: ID first, TAGS second. No flags exist on either
// command — the comma-separated TAGS list captures every value.
func resolveTagPositional(name string, args []string) (string, string, error) {
	if len(args) == 0 || args[0] == "" {
		return "", "", fmt.Errorf("%s: task ID required: %w", name, errors.ErrUsage)
	}
	if len(args) < 2 || args[1] == "" {
		return "", "", fmt.Errorf("%s: TAGS argument required: %w", name, errors.ErrUsage)
	}
	if len(args) > 2 {
		return "", "", fmt.Errorf("%s: unexpected positional arguments: %w", name, errors.ErrUsage)
	}
	return args[0], args[1], nil
}

// parseTagHelpFlags runs `tag` / `untag` args through a FlagSet that
// has no flags of its own so `--help` anywhere in the argv short-
// circuits per STANDARDS.md §`--help` Convention. Both commands take
// two leading positionals (TASK, TAGS) — fs.Parse stops at the first
// non-flag token, so we strip leading positionals in a loop to let
// flag.ErrHelp surface regardless of whether `--help` comes before,
// between, or after the positionals. Returns the collapsed positional
// slice for resolveTagPositional to validate.
func parseTagHelpFlags(name string, stderr io.Writer, args []string) ([]string, error) {
	fs := newFlagSet(name)
	fs.SetOutput(stderr)

	remaining := args
	var positional []string
	for {
		if err := fs.Parse(remaining); err != nil {
			if stderrors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, fmt.Errorf("%s: %s: %w", name, err.Error(), errors.ErrUsage)
		}
		leftover := fs.Args()
		if len(leftover) == 0 {
			break
		}
		positional = append(positional, leftover[0])
		remaining = leftover[1:]
	}
	return positional, nil
}

// Tag adds tags to a task. Tags are validated pre-tx (exit 2 on first
// offender), normalized to lowercase, deduplicated, and inserted with
// INSERT OR IGNORE so repeats are no-ops. History fires only when at
// least one row changed; the ack always emits the full post-state list.
func Tag(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	positional, err := parseTagHelpFlags("tag", stderr, args)
	if stderrors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	id, raw, err := resolveTagPositional("tag", positional)
	if err != nil {
		return err
	}
	tags, err := batch.NormalizeTagList(raw)
	if err != nil {
		return fmt.Errorf("tag: %w", err)
	}
	return tagApply(ctx, cfg, s, stdout, id, tags, store.TxTag, true)
}

// tagApply is the shared transaction body for `tag` and `untag`. The
// handler-supplied add flag selects INSERT OR IGNORE vs DELETE; every
// other rule (existence check first, idempotent rows, history skip
// when no changes, post-state ack) is identical.
func tagApply(ctx context.Context, cfg config.Config, s store.Store, stdout io.Writer, id string, tags []string, kind store.TxKind, add bool) error {
	tx, err := s.BeginImmediate(ctx, kind)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var tier sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT tier FROM tasks WHERE id = ?`, id).
		Scan(&tier)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %q", errors.ErrNotFound, id)
		}
		return fmt.Errorf("%w: tag: %s", errors.ErrGeneral, err.Error())
	}
	telemetry.RecordTaskContext(ctx, id, tier.String)

	now := time.Now().UTC().Format(time.RFC3339)
	var changed []string
	for _, t := range tags {
		var res sql.Result
		var execErr error
		if add {
			res, execErr = tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO tags(task_id, tag) VALUES (?, ?)`, id, t)
		} else {
			res, execErr = tx.ExecContext(ctx,
				`DELETE FROM tags WHERE task_id = ? AND tag = ?`, id, t)
		}
		if execErr != nil {
			return fmt.Errorf("%w: tag: write: %s", errors.ErrGeneral, execErr.Error())
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = append(changed, t)
		}
	}

	if len(changed) > 0 {
		action := store.HistoryTagged
		if !add {
			action = store.HistoryUntagged
		}
		if err := store.AppendHistory(ctx, tx, store.History{
			TaskID:    id,
			Timestamp: now,
			Role:      cfg.Agent.Role,
			Session:   cfg.Agent.Session,
			Action:    action,
			Payload:   map[string]any{"tags": changed},
		}); err != nil {
			return err
		}
	}

	post, err := readTagsTx(ctx, tx, id)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	return output.Emit(stdout, cfg.Output.Text, tagAck{ID: id, Tags: post})
}

// readTagsTx reads the post-state tag list inside the transaction so
// the ack reflects the row set after the writes above. Sorted
// alphabetically per spec §quest show ("sorted, lowercase").
func readTagsTx(ctx context.Context, tx *store.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT tag FROM tags WHERE task_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, fmt.Errorf("%w: tag: read tags: %s", errors.ErrGeneral, err.Error())
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("%w: tag: scan: %s", errors.ErrGeneral, err.Error())
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: tag: iter: %s", errors.ErrGeneral, err.Error())
	}
	sort.Strings(out)
	return out, nil
}

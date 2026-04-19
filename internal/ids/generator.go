package ids

import (
	"context"
	"fmt"
	"strconv"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// NewTopLevel allocates the next short id for prefix inside tx. The
// INSERT ... ON CONFLICT ... RETURNING pattern is one round-trip and
// structurally forbids collisions under concurrent BeginImmediate
// writers — the counter row is updated by exactly one tx at a time and
// the returned value is the post-increment state. prefix formatting is
// already validated at config load via ValidatePrefix; NewTopLevel
// assumes it is well-formed.
func NewTopLevel(ctx context.Context, tx *store.Tx, prefix string) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("%w: ids.NewTopLevel: nil transaction", errors.ErrGeneral)
	}
	var n int64
	row := tx.QueryRowContext(ctx,
		`INSERT INTO task_counter(prefix, next_value) VALUES (?, 1)
		 ON CONFLICT(prefix) DO UPDATE SET next_value = task_counter.next_value + 1
		 RETURNING next_value`,
		prefix,
	)
	if err := row.Scan(&n); err != nil {
		return "", fmt.Errorf("%w: ids.NewTopLevel: %s", errors.ErrGeneral, err.Error())
	}
	return prefix + "-" + formatBase36(n), nil
}

// NewSubTask allocates the next `.N` sub-task id for parent inside tx.
// Depth enforcement is the caller's job — create / batch / move all
// invoke ValidateDepth against the proposed id before reaching here per
// quest-spec §Graph Limits. The counter is keyed on parent id rather
// than (prefix, depth) so sibling sub-task numbers restart at 1 under
// each parent, which matches the spec's "separate per-parent base10
// counter, starting at `1`" rule.
func NewSubTask(ctx context.Context, tx *store.Tx, parent string) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("%w: ids.NewSubTask: nil transaction", errors.ErrGeneral)
	}
	if parent == "" {
		return "", fmt.Errorf("%w: ids.NewSubTask: empty parent id", errors.ErrGeneral)
	}
	var n int64
	row := tx.QueryRowContext(ctx,
		`INSERT INTO subtask_counter(parent_id, next_value) VALUES (?, 1)
		 ON CONFLICT(parent_id) DO UPDATE SET next_value = subtask_counter.next_value + 1
		 RETURNING next_value`,
		parent,
	)
	if err := row.Scan(&n); err != nil {
		return "", fmt.Errorf("%w: ids.NewSubTask: %s", errors.ErrGeneral, err.Error())
	}
	return parent + "." + strconv.FormatInt(n, 10), nil
}

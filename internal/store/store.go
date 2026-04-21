package store

import "context"

// Store is the storage interface every command handler talks to. The
// surface is deliberately narrow — reads plus BeginImmediate plus
// CurrentSchemaVersion. Write handlers own their UPDATE/INSERT SQL
// against the *Tx returned by BeginImmediate, so the interface does
// not include coarse methods like SetStatus or AppendNote (each
// command's SQL shape is bespoke). The InstrumentedStore decorator in
// Task 12.4 wraps this interface one method at a time.
type Store interface {
	// Reads
	GetTask(ctx context.Context, id string) (Task, error)
	GetTaskWithDeps(ctx context.Context, id string) (Task, error)
	ListTasks(ctx context.Context, filter Filter) ([]Task, error)
	GetHistory(ctx context.Context, id string) ([]History, error)
	GetChildren(ctx context.Context, parentID string) ([]Task, error)
	GetDependencies(ctx context.Context, id string) ([]Dependency, error)
	GetDependents(ctx context.Context, id string) ([]Dependency, error)
	GetTags(ctx context.Context, id string) ([]string, error)
	GetPRs(ctx context.Context, id string) ([]PR, error)
	GetCommits(ctx context.Context, id string) ([]Commit, error)
	GetNotes(ctx context.Context, id string) ([]Note, error)

	// Lifecycle
	Close() error
	BeginImmediate(ctx context.Context, kind TxKind) (*Tx, error)
	CurrentSchemaVersion(ctx context.Context) (int, error)
	Snapshot(ctx context.Context, dstPath string) (int64, error)
}

// TxKind labels a BEGIN IMMEDIATE transaction for the quest.store.tx
// span attribute and the dept.quest.store.tx.duration{tx_kind}
// histogram. The enum matches OTEL.md §4.3 / §5.3 — adding a value is
// a telemetry contract change and requires a matching plan update.
type TxKind string

const (
	TxAccept          TxKind = "accept"
	TxCreate          TxKind = "create"
	TxComplete        TxKind = "complete"
	TxFail            TxKind = "fail"
	TxReset           TxKind = "reset"
	TxCancel          TxKind = "cancel"
	TxCancelRecursive TxKind = "cancel_recursive"
	TxMove            TxKind = "move"
	TxBatchCreate     TxKind = "batch_create"
	TxLink            TxKind = "link"
	TxUnlink          TxKind = "unlink"
	TxTag             TxKind = "tag"
	TxUntag           TxKind = "untag"
	TxUpdate          TxKind = "update"
)

// TxOutcome labels the terminal state of a BEGIN IMMEDIATE transaction
// for the quest.tx.outcome span attribute. rolled_back_precondition vs
// rolled_back_error is the dashboard distinction between expected
// spec failures (exit 5) and unexpected bugs.
type TxOutcome string

const (
	TxCommitted              TxOutcome = "committed"
	TxRolledBackPrecondition TxOutcome = "rolled_back_precondition"
	TxRolledBackError        TxOutcome = "rolled_back_error"
)

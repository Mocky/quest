package store

// Store is the storage interface every command handler talks to. Method
// signatures are declared in Task 3.3 alongside the task/history/dep
// types and the *sqliteStore implementation. Phase 2 exposes the type
// identifier so internal/telemetry/ can reference it (telemetry.WrapStore
// takes a Store and returns a Store).
type Store interface{}

// TxKind labels a BEGIN IMMEDIATE transaction for the quest.store.tx
// span attribute and the dept.quest.store.tx.duration{tx_kind}
// histogram. The enum matches OTEL.md §4.3 / §5.3 — adding a value is a
// telemetry contract change and requires a matching plan update.
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
// rolled_back_error is the dashboard distinction between expected spec
// failures (exit 5) and unexpected bugs.
type TxOutcome string

const (
	TxCommitted              TxOutcome = "committed"
	TxRolledBackPrecondition TxOutcome = "rolled_back_precondition"
	TxRolledBackError        TxOutcome = "rolled_back_error"
)

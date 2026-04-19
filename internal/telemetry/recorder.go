package telemetry

import (
	"context"
	"io"
	stdlog "log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/mocky/quest/internal/errors"
)

// Instrument package-level handles. Most land in Task 12.5;
// schemaMigrationsCtr arrives with Task 12.1's MigrateSpan, and
// operationsCtr lands with Task 12.2's WrapCommand. nil checks keep
// the calling sites safe even when an instrument has not been
// registered yet (e.g., disabled-OTEL path or mid-phase build).
var (
	schemaMigrationsCtr metric.Int64Counter
	operationsCtr       metric.Int64Counter
)

// initSchemaMigrationsInstrument is invoked by Setup once the meter
// provider is installed. Held separate from a future initInstruments
// helper so Task 12.1 can land the migration span/metric without
// depending on the rest of Task 12.5's instrument table.
func initSchemaMigrationsInstrument() {
	c, err := meter.Int64Counter("dept.quest.schema.migrations",
		metric.WithDescription("Schema migrations applied."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		stdlog.Warn("instrument", "name", "dept.quest.schema.migrations", "err", err)
		return
	}
	schemaMigrationsCtr = c

	op, err := meter.Int64Counter("dept.quest.operations",
		metric.WithDescription("Total CLI invocations by command and outcome."),
		metric.WithUnit("{operation}"),
	)
	if err != nil {
		stdlog.Warn("instrument", "name", "dept.quest.operations", "err", err)
		return
	}
	operationsCtr = op
}

// statusAttrs caches the {status} attribute set for
// dept.quest.operations to avoid building the slice on every call.
// Task 12.5 may convert these to package-level attribute.KeyValue
// constants.
func statusAttrs(status string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("status", status))
}

// StoreSpan opens a child span under the active command span for
// store-level operations (`quest.store.traverse`,
// `quest.store.rename_subgraph`). Handlers call it when they need a
// named child span without importing go.opentelemetry.io/otel/trace.
// Phase 2 returns ctx and a no-op end closure; Task 12 fills in the
// real tracer.Start + three-step error recording.
func StoreSpan(ctx context.Context, name string) (context.Context, func(err error)) {
	_ = name
	return ctx, func(err error) { _ = err }
}

// RecordTaskContext sets the §4.3 task-affecting attributes
// (quest.task.id, quest.task.tier, quest.task.type) on the active
// command span. Called by every task-affecting handler (show, accept,
// update, complete, fail, cancel, move, deps, tag, untag, graph).
func RecordTaskContext(ctx context.Context, id, tier, taskType string) {
	_ = ctx
	_ = id
	_ = tier
	_ = taskType
}

// RecordHandlerError implements the §4.4 + §13 pattern on the active
// span: span.RecordError(err), span.SetStatus(codes.Error, ...), and
// sets quest.error.class, quest.error.retryable, quest.exit_code from
// the err's class/exit-code mapping. Increments
// dept.quest.errors{error_class}. Called from WrapCommand and via
// RecordDispatchError. Body fills in at Task 12.5.
func RecordHandlerError(ctx context.Context, err error) {
	_ = ctx
	_ = err
}

// RecordDispatchError is the dispatcher-side helper that replaces any
// ad-hoc errorExit inside internal/cli/. It calls RecordHandlerError,
// increments dept.quest.operations{status=error}, emits the "internal
// error" slog record (OTEL.md §3.2 — same canonical message as handler
// errors; optional origin="dispatch" attribute distinguishes them),
// writes the stderr two-liner via errors.EmitStderr, and returns
// errors.ExitCode(err). Lives here so internal/cli/ never imports OTEL
// (§10.1). Task 12.5 wires the span/counter/slog enrichments behind
// this call; the Phase-4 stub just emits stderr + maps the exit code.
func RecordDispatchError(ctx context.Context, err error, stderr io.Writer) int {
	_ = ctx
	if err == nil {
		return 0
	}
	errors.EmitStderr(err, stderr)
	return errors.ExitCode(err)
}

// RecordPreconditionFailed emits the §13.3 quest.precondition.failed
// span event with quest.precondition (bounded enum),
// quest.blocked_by_count, and a truncated quest.blocked_by_ids. Called
// on every exit-5 path in handlers per cross-cutting.md.
func RecordPreconditionFailed(ctx context.Context, precondition string, blockedByIDs []string) {
	_ = ctx
	_ = precondition
	_ = blockedByIDs
}

// RecordCycleDetected emits the §13.4 quest.dep.cycle_detected span
// event with quest.cycle.path (truncated at 512 chars per §13.4 via
// truncateIDList) and quest.cycle.length. Called by `quest link
// --blocked-by` and the `quest batch` graph phase.
func RecordCycleDetected(ctx context.Context, path []string) {
	_ = ctx
	_ = path
}

// RecordTerminalState emits dept.quest.tasks.completed{tier, role,
// outcome} for every terminal-state arrival. complete/fail call it
// once; cancel -r calls it once per descendant transitioned.
func RecordTerminalState(ctx context.Context, taskID, tier, role, outcome string) {
	_ = ctx
	_ = taskID
	_ = tier
	_ = role
	_ = outcome
}

// RecordTaskCreated enriches the active span with the new task's
// identity (quest.task.id/tier/role/type) and increments
// dept.quest.tasks.created{tier, role, type}. OTEL.md §8.6.
func RecordTaskCreated(ctx context.Context, taskID, tier, role, taskType string) {
	_ = ctx
	_ = taskID
	_ = tier
	_ = role
	_ = taskType
}

// RecordStatusTransition sets quest.task.status.from/to on the span
// and increments dept.quest.status_transitions{from, to}. OTEL.md
// §8.6. Called by every status-changing handler.
func RecordStatusTransition(ctx context.Context, taskID, from, to string) {
	_ = ctx
	_ = taskID
	_ = from
	_ = to
}

// RecordLinkAdded / RecordLinkRemoved increment
// dept.quest.links{link_type, action}. OTEL.md §5.1.
func RecordLinkAdded(ctx context.Context, taskID, targetID, linkType string) {
	_ = ctx
	_ = taskID
	_ = targetID
	_ = linkType
}

func RecordLinkRemoved(ctx context.Context, taskID, targetID, linkType string) {
	_ = ctx
	_ = taskID
	_ = targetID
	_ = linkType
}

// RecordBatchOutcome records batch.size + outcome (ok/partial/rejected)
// and increments dept.quest.batch.size. OTEL.md §5.1. Phase-2 signature
// preserved (created, errors, outcome) until Task 12.11 swaps in the
// derived-outcome variant.
func RecordBatchOutcome(ctx context.Context, created, errors int, outcome string) {
	_ = ctx
	_ = created
	_ = errors
	_ = outcome
}

// RecordBatchError emits one quest.batch.error span event per
// per-line validation error on the active phase span, with
// phase/line/code/field/ref attributes. Also increments
// dept.quest.batch.errors{phase, code}. OTEL.md §4.4 / §8.5.
func RecordBatchError(ctx context.Context, phase, code, field, ref string, line int) {
	_ = ctx
	_ = phase
	_ = code
	_ = field
	_ = ref
	_ = line
}

// RecordMoveOutcome records quest.move.subgraph_size and the dependency
// rows rewritten by the FK cascade on the command span. OTEL.md §8.6.
// oldID / newID are the moved task's IDs before and after the rename;
// subgraphSize is the count of tasks renamed; depUpdates is the count
// of dependencies rows the ON UPDATE CASCADE rewrote (computed via a
// pre-rename COUNT since sql.Result.RowsAffected does not see cascade
// side-effects).
func RecordMoveOutcome(ctx context.Context, oldID, newID string, subgraphSize, depUpdates int) {
	_ = ctx
	_ = oldID
	_ = newID
	_ = subgraphSize
	_ = depUpdates
}

// RecordCancelOutcome records cancel / cancel_recursive span attributes
// on the command span. cancelledCount is the number of tasks
// transitioned to cancelled by this call (including the root);
// skippedCount is the number of already-terminal descendants skipped.
// OTEL.md §8.6.
func RecordCancelOutcome(ctx context.Context, taskID string, recursive bool, cancelledCount, skippedCount int) {
	_ = ctx
	_ = taskID
	_ = recursive
	_ = cancelledCount
	_ = skippedCount
}

// RecordContentReason emits a `quest.content.reason` span event when
// the OTEL_GENAI_CAPTURE_CONTENT toggle is on. Callers gate on
// CaptureContentEnabled() before invoking so the no-op path never pays
// allocation cost for the truncation helper. Task 12.7 fills in the
// span event body.
func RecordContentReason(ctx context.Context, reason string) {
	_ = ctx
	_ = reason
}

// RecordQueryResult records dept.quest.query.result_count{command} for
// list/deps. OTEL.md §5.1.
func RecordQueryResult(ctx context.Context, command string, count int) {
	_ = ctx
	_ = command
	_ = count
}

// RecordGraphResult records dept.quest.query.result_count and
// dept.quest.graph.traversal_nodes for `quest graph`. OTEL.md §5.1.
func RecordGraphResult(ctx context.Context, nodesReturned, nodesVisited int) {
	_ = ctx
	_ = nodesReturned
	_ = nodesVisited
}

// Suppress unused-import warnings while Task 12.5 / 12.6 / 12.7 / 12.9
// fill in the bodies. These are all standard OTEL types the recorder
// bodies will consume; importing them now keeps subsequent task
// commits focused on body changes rather than imports.
var (
	_ = attribute.String
	_ = codes.Error
	_ trace.Span
)

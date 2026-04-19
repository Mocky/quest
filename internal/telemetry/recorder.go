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

// Instrument package-level handles. Setup invokes initSchemaMigrationsInstrument
// (which expanded into the full §5.1 table at Task 12.5). nil checks keep
// every call site safe on the disabled-OTEL path even though Setup
// always populates these when telemetry is enabled.
var (
	schemaMigrationsCtr  metric.Int64Counter
	operationsCtr        metric.Int64Counter
	errorsCtr            metric.Int64Counter
	tasksCreatedCtr      metric.Int64Counter
	tasksCompletedCtr    metric.Int64Counter
	statusTransitionsCtr metric.Int64Counter
	linksCtr             metric.Int64Counter
	batchSizeHis         metric.Int64Histogram
	batchErrorsCtr       metric.Int64Counter
	storeTxDurationHis   metric.Float64Histogram
	storeTxLockWaitHis   metric.Float64Histogram
	storeLockTimeoutsCtr metric.Int64Counter
	queryResultCountHis  metric.Int64Histogram
	graphTraversalHis    metric.Int64Histogram
	operationDurationHis metric.Float64Histogram
)

// initSchemaMigrationsInstrument is the single seam Setup calls after
// the meter provider is installed. The historical name remains so the
// Task 12.1 commit log stays accurate; the body now registers every
// instrument in OTEL.md §5.1.
func initSchemaMigrationsInstrument() {
	register := func(name string, build func() error) {
		if err := build(); err != nil {
			stdlog.Warn("instrument", "name", name, "err", err)
		}
	}

	register("dept.quest.schema.migrations", func() error {
		c, err := meter.Int64Counter("dept.quest.schema.migrations",
			metric.WithDescription("Schema migrations applied."),
			metric.WithUnit("{event}"))
		if err == nil {
			schemaMigrationsCtr = c
		}
		return err
	})
	register("dept.quest.operations", func() error {
		c, err := meter.Int64Counter("dept.quest.operations",
			metric.WithDescription("Total CLI invocations by command and outcome."),
			metric.WithUnit("{operation}"))
		if err == nil {
			operationsCtr = c
		}
		return err
	})
	register("dept.quest.operation.duration", func() error {
		h, err := meter.Float64Histogram("dept.quest.operation.duration",
			metric.WithDescription("Latency distribution per command."),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000))
		if err == nil {
			operationDurationHis = h
		}
		return err
	})
	register("dept.quest.errors", func() error {
		c, err := meter.Int64Counter("dept.quest.errors",
			metric.WithDescription("Errored invocations by command and error class."),
			metric.WithUnit("{operation}"))
		if err == nil {
			errorsCtr = c
		}
		return err
	})
	register("dept.quest.tasks.created", func() error {
		c, err := meter.Int64Counter("dept.quest.tasks.created",
			metric.WithDescription("Tasks created via quest create or quest batch."),
			metric.WithUnit("{task}"))
		if err == nil {
			tasksCreatedCtr = c
		}
		return err
	})
	register("dept.quest.tasks.completed", func() error {
		c, err := meter.Int64Counter("dept.quest.tasks.completed",
			metric.WithDescription("Tasks reaching a terminal state."),
			metric.WithUnit("{task}"))
		if err == nil {
			tasksCompletedCtr = c
		}
		return err
	})
	register("dept.quest.status_transitions", func() error {
		c, err := meter.Int64Counter("dept.quest.status_transitions",
			metric.WithDescription("All status transitions."),
			metric.WithUnit("{task}"))
		if err == nil {
			statusTransitionsCtr = c
		}
		return err
	})
	register("dept.quest.links", func() error {
		c, err := meter.Int64Counter("dept.quest.links",
			metric.WithDescription("Dependency link additions and removals."),
			metric.WithUnit("{link}"))
		if err == nil {
			linksCtr = c
		}
		return err
	})
	register("dept.quest.batch.size", func() error {
		h, err := meter.Int64Histogram("dept.quest.batch.size",
			metric.WithDescription("Tasks-per-batch distribution."),
			metric.WithUnit("{task}"),
			metric.WithExplicitBucketBoundaries(1, 2, 5, 10, 25, 50, 100, 250, 500))
		if err == nil {
			batchSizeHis = h
		}
		return err
	})
	register("dept.quest.batch.errors", func() error {
		c, err := meter.Int64Counter("dept.quest.batch.errors",
			metric.WithDescription("Batch validation errors by phase and error code."),
			metric.WithUnit("{event}"))
		if err == nil {
			batchErrorsCtr = c
		}
		return err
	})
	register("dept.quest.store.tx.duration", func() error {
		h, err := meter.Float64Histogram("dept.quest.store.tx.duration",
			metric.WithDescription("BEGIN IMMEDIATE transaction duration by kind."),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000))
		if err == nil {
			storeTxDurationHis = h
		}
		return err
	})
	register("dept.quest.store.tx.lock_wait", func() error {
		h, err := meter.Float64Histogram("dept.quest.store.tx.lock_wait",
			metric.WithDescription("Time spent waiting for the SQLite write lock."),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(0, 1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000))
		if err == nil {
			storeTxLockWaitHis = h
		}
		return err
	})
	register("dept.quest.store.lock_timeouts", func() error {
		c, err := meter.Int64Counter("dept.quest.store.lock_timeouts",
			metric.WithDescription("Operations that exited with code 7."),
			metric.WithUnit("{operation}"))
		if err == nil {
			storeLockTimeoutsCtr = c
		}
		return err
	})
	register("dept.quest.query.result_count", func() error {
		h, err := meter.Int64Histogram("dept.quest.query.result_count",
			metric.WithDescription("Result counts for list, graph, deps."),
			metric.WithUnit("{task}"),
			metric.WithExplicitBucketBoundaries(0, 1, 5, 10, 25, 50, 100, 250, 500, 1000))
		if err == nil {
			queryResultCountHis = h
		}
		return err
	})
	register("dept.quest.graph.traversal_nodes", func() error {
		h, err := meter.Int64Histogram("dept.quest.graph.traversal_nodes",
			metric.WithDescription("Nodes visited during graph traversal."),
			metric.WithUnit("{task}"),
			metric.WithExplicitBucketBoundaries(0, 1, 5, 10, 25, 50, 100, 250, 500, 1000))
		if err == nil {
			graphTraversalHis = h
		}
		return err
	})
}

// statusAttrs caches the {status} attribute set for
// dept.quest.operations. Constant-string attributes are cheap; the
// helper keeps the call site readable.
func statusAttrs(status string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("status", status))
}

// StoreSpan lives in store.go (Task 12.4) — the helper opens a child
// span under the active command span for graph/move traversals.

// RecordTaskContext sets the §4.3 task-affecting attributes
// (quest.task.id, quest.task.tier, quest.task.type) on the active
// command span. Called by every task-affecting handler.
func RecordTaskContext(ctx context.Context, id, tier, taskType string) {
	span := trace.SpanFromContext(ctx)
	if nonRecording(span) {
		return
	}
	attrs := make([]attribute.KeyValue, 0, 3)
	if id != "" {
		attrs = append(attrs, attribute.String("quest.task.id", id))
	}
	if tier != "" {
		attrs = append(attrs, attribute.String("quest.task.tier", tier))
	}
	if taskType != "" {
		attrs = append(attrs, attribute.String("quest.task.type", taskType))
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
}

// RecordHandlerError implements the §4.4 + §13 pattern on the active
// span: span.RecordError(err), span.SetStatus(codes.Error, ...), and
// sets quest.error.class, quest.error.retryable, quest.exit_code from
// the err's class/exit-code mapping. Increments
// dept.quest.errors{error_class}. Called from WrapCommand and via
// RecordDispatchError — single source of truth for error-attribute
// application.
func RecordHandlerError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	class := errors.Class(err)
	exitCode := errors.ExitCode(err)
	retryable := errors.Retryable(err)

	span := trace.SpanFromContext(ctx)
	if !nonRecording(span) {
		span.RecordError(err)
		span.SetStatus(codes.Error, Truncate(err.Error(), 256))
		span.SetAttributes(
			attribute.String("quest.error.class", class),
			attribute.Bool("quest.error.retryable", retryable),
			attribute.Int("quest.exit_code", exitCode),
		)
	}
	if errorsCtr != nil {
		errorsCtr.Add(ctx, 1, metric.WithAttributes(
			attribute.String("error_class", class),
		))
	}
}

// RecordDispatchError is the dispatcher-side helper that replaces any
// ad-hoc errorExit inside internal/cli/. It calls RecordHandlerError,
// increments dept.quest.operations{status=error}, emits the "internal
// error" slog record (OTEL.md §3.2 — same canonical message as handler
// errors; origin="dispatch" attribute distinguishes them from handler
// panics), writes the stderr two-liner via errors.EmitStderr, and
// returns errors.ExitCode(err). Lives here so internal/cli/ never
// imports OTEL (§10.1).
func RecordDispatchError(ctx context.Context, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	RecordHandlerError(ctx, err)
	if operationsCtr != nil {
		operationsCtr.Add(ctx, 1, statusAttrs("error"))
	}
	stdlog.ErrorContext(ctx, "internal error",
		"err", Truncate(err.Error(), 256),
		"class", errors.Class(err),
		"origin", "dispatch",
	)
	errors.EmitStderr(err, stderr)
	return errors.ExitCode(err)
}

// RecordPreconditionFailed emits the §13.3 quest.precondition.failed
// span event with quest.precondition (bounded enum),
// quest.blocked_by_count, and a truncated quest.blocked_by_ids. Called
// on every exit-5 path in handlers per cross-cutting.md.
func RecordPreconditionFailed(ctx context.Context, precondition string, blockedByIDs []string) {
	span := trace.SpanFromContext(ctx)
	if nonRecording(span) {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("quest.precondition", precondition),
	}
	if len(blockedByIDs) > 0 {
		ids := blockedByIDs
		if len(ids) > 10 {
			ids = ids[:10]
		}
		attrs = append(attrs,
			attribute.Int("quest.blocked_by_count", len(blockedByIDs)),
			attribute.String("quest.blocked_by_ids", truncateIDList(ids, 256)),
		)
	}
	span.AddEvent("quest.precondition.failed", trace.WithAttributes(attrs...))
}

// RecordCycleDetected emits the §13.4 quest.dep.cycle_detected span
// event with quest.cycle.path (truncated at 512 chars per §13.4 via
// truncateIDList) and quest.cycle.length. Called by `quest link
// --blocked-by` and the `quest batch` graph phase.
func RecordCycleDetected(ctx context.Context, path []string) {
	span := trace.SpanFromContext(ctx)
	if nonRecording(span) {
		return
	}
	span.AddEvent("quest.dep.cycle_detected", trace.WithAttributes(
		attribute.String("quest.cycle.path", truncateIDList(path, 512)),
		attribute.Int("quest.cycle.length", len(path)),
	))
}

// RecordTerminalState emits dept.quest.tasks.completed{tier, role,
// outcome} for every terminal-state arrival. complete/fail call it
// once; cancel -r calls it once per descendant transitioned.
func RecordTerminalState(ctx context.Context, taskID, tier, role, outcome string) {
	_ = taskID
	if tasksCompletedCtr == nil {
		return
	}
	tasksCompletedCtr.Add(ctx, 1, metric.WithAttributes(
		attribute.String("tier", tier),
		attribute.String("role", roleOrUnset(role)),
		attribute.String("outcome", outcome),
	))
}

// RecordTaskCreated enriches the active span with the new task's
// identity (quest.task.id/tier/role/type) and increments
// dept.quest.tasks.created{tier, role, type}. OTEL.md §8.6.
func RecordTaskCreated(ctx context.Context, taskID, tier, role, taskType string) {
	span := trace.SpanFromContext(ctx)
	if !nonRecording(span) {
		span.SetAttributes(
			attribute.String("quest.task.id", taskID),
			attribute.String("quest.task.tier", tier),
			attribute.String("quest.task.role", roleOrUnset(role)),
			attribute.String("quest.task.type", taskType),
		)
	}
	if tasksCreatedCtr != nil {
		tasksCreatedCtr.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tier", tier),
			attribute.String("role", roleOrUnset(role)),
			attribute.String("type", taskType),
		))
	}
}

// RecordStatusTransition sets quest.task.status.from/to on the span
// and increments dept.quest.status_transitions{from, to}. OTEL.md §8.6.
func RecordStatusTransition(ctx context.Context, taskID, from, to string) {
	_ = taskID
	span := trace.SpanFromContext(ctx)
	if !nonRecording(span) {
		span.SetAttributes(
			attribute.String("quest.task.status.from", from),
			attribute.String("quest.task.status.to", to),
		)
	}
	if statusTransitionsCtr != nil {
		statusTransitionsCtr.Add(ctx, 1, metric.WithAttributes(
			attribute.String("from", from),
			attribute.String("to", to),
		))
	}
}

// RecordLinkAdded / RecordLinkRemoved increment
// dept.quest.links{link_type, action}. OTEL.md §5.1.
func RecordLinkAdded(ctx context.Context, taskID, targetID, linkType string) {
	_ = taskID
	_ = targetID
	if linksCtr == nil {
		return
	}
	linksCtr.Add(ctx, 1, metric.WithAttributes(
		attribute.String("link_type", linkType),
		attribute.String("action", "added"),
	))
}

func RecordLinkRemoved(ctx context.Context, taskID, targetID, linkType string) {
	_ = taskID
	_ = targetID
	if linksCtr == nil {
		return
	}
	linksCtr.Add(ctx, 1, metric.WithAttributes(
		attribute.String("link_type", linkType),
		attribute.String("action", "removed"),
	))
}

// RecordBatchOutcome records batch.size + outcome (ok/partial/rejected)
// + the §4.3 batch span attributes. Phase-2 signature preserved
// (created, errors, outcome) — Task 12.11 swaps in the derived-outcome
// variant that takes lines_total / lines_blank / partial_ok.
func RecordBatchOutcome(ctx context.Context, created, errCount int, outcome string) {
	span := trace.SpanFromContext(ctx)
	if !nonRecording(span) {
		span.SetAttributes(
			attribute.Int("quest.batch.created", created),
			attribute.Int("quest.batch.errors", errCount),
			attribute.String("quest.batch.outcome", outcome),
		)
	}
	if batchSizeHis != nil {
		batchSizeHis.Record(ctx, int64(created), metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
}

// RecordBatchError emits one quest.batch.error span event per
// per-line validation error on the active phase span, with
// phase/line/code/field/ref attributes. Also increments
// dept.quest.batch.errors{phase, code}. OTEL.md §4.4 / §8.5.
func RecordBatchError(ctx context.Context, phase, code, field, ref string, line int) {
	span := trace.SpanFromContext(ctx)
	if !nonRecording(span) {
		attrs := []attribute.KeyValue{
			attribute.String("phase", phase),
			attribute.String("code", code),
			attribute.Int("line", line),
		}
		if field != "" {
			attrs = append(attrs, attribute.String("field", field))
		}
		if ref != "" {
			attrs = append(attrs, attribute.String("ref", ref))
		}
		span.AddEvent("quest.batch.error", trace.WithAttributes(attrs...))
	}
	if batchErrorsCtr != nil {
		batchErrorsCtr.Add(ctx, 1, metric.WithAttributes(
			attribute.String("phase", phase),
			attribute.String("code", code),
		))
	}
}

// RecordMoveOutcome records quest.move.* span attributes for the
// `quest move` command. subgraphSize is the count of tasks renamed
// (self + descendants); depUpdates is the count of dependencies-table
// rows the FK cascade rewrote. (OTEL.md §8.6 / §4.3.)
func RecordMoveOutcome(ctx context.Context, oldID, newID string, subgraphSize, depUpdates int) {
	span := trace.SpanFromContext(ctx)
	if nonRecording(span) {
		return
	}
	span.SetAttributes(
		attribute.String("quest.move.old_id", oldID),
		attribute.String("quest.move.new_id", newID),
		attribute.Int("quest.move.subgraph_size", subgraphSize),
		attribute.Int("quest.move.dep_updates", depUpdates),
	)
}

// RecordCancelOutcome records cancel / cancel_recursive span
// attributes. taskID is the cancel target (also emitted as the
// task-affecting quest.task.id row per §4.3 — no proprietary
// quest.cancel.target_id duplicates it). cancelledCount is the
// number of tasks transitioned to cancelled by this call (including
// the root); skippedCount is the number of already-terminal
// descendants skipped. (OTEL.md §8.6 / §4.3.)
func RecordCancelOutcome(ctx context.Context, taskID string, recursive bool, cancelledCount, skippedCount int) {
	span := trace.SpanFromContext(ctx)
	if nonRecording(span) {
		return
	}
	span.SetAttributes(
		attribute.String("quest.task.id", taskID),
		attribute.Bool("quest.cancel.recursive", recursive),
		attribute.Int("quest.cancel.cancelled_count", cancelledCount),
		attribute.Int("quest.cancel.skipped_count", skippedCount),
	)
}

// RecordContentReason and the rest of the content recorders live in
// content.go (Task 12.7).

// QueryFilter carries the bounded-enum filter values from a list/deps
// invocation. Each slice holds the accepted filter values as resolved
// by the handler — sorted/joined inside the recorder so dashboards see
// a stable string. Tag and parent filters are deliberately absent
// (OTEL.md §4.3 — unbounded cardinality).
type QueryFilter struct {
	Status []string
	Role   []string
	Tier   []string
	Type   []string
	Ready  bool
}

// RecordQueryResult records dept.quest.query.result_count{command} +
// the §4.3 query span attributes for list/deps. The filter argument
// carries bounded-enum filter values; tag and parent filters are
// excluded by spec (OTEL.md §4.3).
func RecordQueryResult(ctx context.Context, operation string, resultCount int, filter QueryFilter) {
	span := trace.SpanFromContext(ctx)
	if !nonRecording(span) {
		attrs := []attribute.KeyValue{
			attribute.Int("quest.query.result_count", resultCount),
		}
		if v := joinSorted(filter.Status); v != "" {
			attrs = append(attrs, attribute.String("quest.query.filter.status", v))
		}
		if v := joinSorted(filter.Role); v != "" {
			attrs = append(attrs, attribute.String("quest.query.filter.role", v))
		}
		if v := joinSorted(filter.Tier); v != "" {
			attrs = append(attrs, attribute.String("quest.query.filter.tier", v))
		}
		if v := joinSorted(filter.Type); v != "" {
			attrs = append(attrs, attribute.String("quest.query.filter.type", v))
		}
		if filter.Ready {
			attrs = append(attrs, attribute.Bool("quest.query.ready", true))
		}
		span.SetAttributes(attrs...)
	}
	if queryResultCountHis != nil {
		queryResultCountHis.Record(ctx, int64(resultCount), metric.WithAttributes(
			attribute.String("command", operation),
		))
	}
}

// RecordGraphResult records dept.quest.graph.traversal_nodes,
// dept.quest.query.result_count, and the §4.3 graph span attributes
// for `quest graph`. quest.task.id carries the rootID per the
// task-affecting attribute row; quest.graph.traversal_nodes lives on
// the metric only, never as a span attribute.
func RecordGraphResult(ctx context.Context, rootID string, nodeCount, edgeCount, externalCount, traversalNodes int) {
	span := trace.SpanFromContext(ctx)
	if !nonRecording(span) {
		span.SetAttributes(
			attribute.String("quest.task.id", rootID),
			attribute.Int("quest.graph.node_count", nodeCount),
			attribute.Int("quest.graph.edge_count", edgeCount),
			attribute.Int("quest.graph.external_count", externalCount),
		)
	}
	if graphTraversalHis != nil {
		graphTraversalHis.Record(ctx, int64(traversalNodes), metric.WithAttributes(
			attribute.String("command", "graph"),
		))
	}
	if queryResultCountHis != nil {
		queryResultCountHis.Record(ctx, int64(nodeCount), metric.WithAttributes(
			attribute.String("command", "graph"),
		))
	}
}

func joinSorted(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	cp := make([]string, len(vs))
	copy(cp, vs)
	sortStrings(cp)
	return joinComma(cp)
}

func sortStrings(vs []string) {
	for i := 1; i < len(vs); i++ {
		j := i
		for j > 0 && vs[j-1] > vs[j] {
			vs[j-1], vs[j] = vs[j], vs[j-1]
			j--
		}
	}
}

func joinComma(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	out := vs[0]
	for _, v := range vs[1:] {
		out += "," + v
	}
	return out
}

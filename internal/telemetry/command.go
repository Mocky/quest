package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// CommandSpan opens the root `execute_tool quest.<cmd>` span with the
// §4.3 attribute set (gen_ai.tool.name, gen_ai.operation.name,
// gen_ai.agent.name from cached identity, dept.task.id,
// dept.session.id, quest.role.elevated). cli.Execute calls it once per
// invocation and defers span.End(); handlers never invoke CommandSpan
// directly (OTEL.md §8.2). When OTEL is disabled the global no-op
// tracer returns a non-recording span so the dispatcher's
// `defer span.End()` and the WrapCommand error path stay cheap.
func CommandSpan(ctx context.Context, command string, elevated bool) (context.Context, trace.Span) {
	return tracer.Start(ctx, "execute_tool quest."+command,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("gen_ai.tool.name", "quest."+command),
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.agent.name", identity.agentRole),
			attribute.String("dept.task.id", identity.agentTask),
			attribute.String("dept.session.id", identity.agentSession),
			attribute.Bool("quest.role.elevated", elevated),
		),
	)
}

// WrapCommand is the dispatcher-owned middleware that runs fn inside
// the already-open command span (picked up via trace.SpanFromContext).
// On a non-nil returned error it applies the §4.4 three-step pattern
// via RecordHandlerError (RecordError + SetStatus + class/exit_code/
// retryable attributes + dept.quest.errors counter). It does NOT
// start or end a span — cli.Execute owns that via CommandSpan plus
// defer span.End(). The dept.quest.operations{status} counter is
// incremented here regardless of error so the success/error split
// stays balanced (OTEL.md §8.2).
func WrapCommand(ctx context.Context, command string, fn func(context.Context) error) error {
	_ = command
	err := fn(ctx)
	if err != nil {
		RecordHandlerError(ctx, err)
		incOperations(ctx, "error")
		return err
	}
	incOperations(ctx, "ok")
	return nil
}

// incOperations increments dept.quest.operations{status}. Stays a
// helper rather than inline so Task 12.5's instrument creation can
// land alongside RecordHandlerError without touching WrapCommand.
func incOperations(ctx context.Context, status string) {
	if operationsCtr == nil {
		return
	}
	operationsCtr.Add(ctx, 1, statusAttrs(status))
}

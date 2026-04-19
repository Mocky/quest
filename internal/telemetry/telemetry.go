package telemetry

import (
	"context"
	"log/slog"
)

type Config struct {
	ServiceName    string
	ServiceVersion string
	AgentRole      string
	AgentTask      string
	AgentSession   string
	CaptureContent bool
}

func Setup(ctx context.Context, cfg Config) (slog.Handler, func(context.Context) error, error) {
	_ = ctx
	_ = cfg
	return nil, func(context.Context) error { return nil }, nil
}

func ExtractTraceFromConfig(ctx context.Context, traceParent, traceState string) context.Context {
	_ = traceParent
	_ = traceState
	return ctx
}

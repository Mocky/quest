package telemetry

import (
	"context"
	"log/slog"
)

// Config carries everything telemetry needs that is sourced from env
// vars — resolved by internal/config/ per STANDARDS.md Part 1.
// telemetry never calls os.Getenv itself (OTEL.md §8.2).
type Config struct {
	ServiceName    string
	ServiceVersion string
	AgentRole      string
	AgentTask      string
	AgentSession   string
	CaptureContent bool
}

// Setup initializes the OTEL SDK and returns the otelslog bridge
// handler for the fan-out in internal/logging/ (OTEL.md §7.1). In
// Phase 2 it installs nothing and returns (nil, no-op shutdown, nil);
// Task 12.1 replaces the body with real provider wiring.
func Setup(ctx context.Context, cfg Config) (bridge slog.Handler, shutdown func(context.Context) error, err error) {
	_ = ctx
	setIdentity(cfg.AgentRole, cfg.AgentTask, cfg.AgentSession)
	setCaptureContent(cfg.CaptureContent)
	return nil, func(context.Context) error { return nil }, nil
}

// Package main is the quest CLI entrypoint. main() is the two-function
// shell required by OTEL.md §7.1: it only calls os.Exit(run()). All real
// work lives in run(), which resolves config, installs the slog default,
// calls telemetry.Setup, extracts the inbound W3C trace context, and
// dispatches to cli.Execute. Command handlers never call os.Exit — they
// return an int exit code so main's deferred OTEL shutdown can flush
// first. See AGENTS.md §Folder structure and OTEL.md §7.1.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/mocky/quest/internal/buildinfo"
	"github.com/mocky/quest/internal/cli"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/logging"
	"github.com/mocky/quest/internal/telemetry"
)

func otelLevel(s string) slog.Level {
	lvl, _ := logging.LevelFromString(s)
	return lvl
}

func main() {
	os.Exit(run())
}

func run() int {
	flags, remainingArgs, err := cli.ParseGlobals(os.Args[1:])
	if err != nil {
		errors.EmitStderr(err, os.Stderr)
		return errors.ExitCode(err)
	}
	cfg := config.Load(flags)

	ctx := context.Background()

	slog.SetDefault(logging.Setup(cfg.Log))

	bridge, otelShutdown, _ := telemetry.Setup(ctx, telemetry.Config{
		ServiceName:    "quest-cli",
		ServiceVersion: buildinfo.Version,
		AgentRole:      cfg.Agent.Role,
		AgentTask:      cfg.Agent.Task,
		AgentSession:   cfg.Agent.Session,
		CaptureContent: cfg.Telemetry.CaptureContent,
		OTELLevel:      otelLevel(cfg.Log.OTELLevel),
	})
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = otelShutdown(shutdownCtx)
	}()

	slog.SetDefault(logging.Setup(cfg.Log, bridge))

	ctx = telemetry.ExtractTraceFromConfig(ctx, cfg.Agent.TraceParent, cfg.Agent.TraceState)

	return cli.Execute(ctx, cfg, remainingArgs, os.Stdin, os.Stdout, os.Stderr)
}

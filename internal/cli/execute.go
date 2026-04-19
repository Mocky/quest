package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// Execute is the single dispatcher entry point called from main.run().
// The caller is responsible for parsing global flags via ParseGlobals
// and for passing a ctx that already carries the inbound W3C trace
// context (extracted by telemetry.ExtractTraceFromConfig). Execute
// handles command identification, role gating, workspace validation,
// store open + migrate, per-command span ownership, handler dispatch,
// and panic recovery. It returns the process exit code — main.run()
// forwards it to os.Exit.
//
// The dispatch sequence is load-bearing (OTEL.md §4.1 span hierarchy,
// spec §Error precedence for role-denial uniformity). Altering the
// order risks breaking contract tests and dashboard semantics.
func Execute(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) (exitCode int) {
	start := time.Now()
	parentCtx := ctx

	// Top-level panic recovery wraps the entire dispatch body so a
	// panic in any phase (pre-handler checks, SuppressTelemetry path,
	// WrapCommand path) is translated into ErrGeneral exit 1 with an
	// ERROR slog record per OTEL.md §3.2. origin="handler" is the
	// best single classification; Phase 12 can refine if dispatcher
	// panics need to be distinguished from handler panics.
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			if len(stack) > 2048 {
				stack = stack[:2048]
			}
			err := fmt.Errorf("%w: panic: %v", errors.ErrGeneral, r)
			slog.ErrorContext(ctx, "internal error",
				"err", telemetry.Truncate(err.Error(), 256),
				"stack", stack,
				"origin", "handler",
			)
			errors.EmitStderr(err, stderr)
			exitCode = errors.ExitCode(err)
		}
	}()

	// Step 1 — identify the command. No args or bare --help prints the
	// role-filtered banner and exits 0; unknown tokens are exit 2
	// usage errors. Per-command --help is handled later by each
	// handler's own FlagSet and does NOT short-circuit the workspace
	// and role checks (plan §Deliberate deviations: --help is gated
	// by the same preconditions as running the command itself).
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printBanner(cfg, stdout)
		return 0
	}

	name := args[0]
	desc, known := lookupDescriptor(name)
	if !known {
		err := fmt.Errorf("%w: %s", errors.ErrUsage, unknownCommandMessage(name, cfg))
		return telemetry.RecordDispatchError(ctx, err, stderr)
	}

	// Step 2 — open the command span. CommandSpan returns ctx with
	// the span attached; we defer End() so every later error path
	// closes the span cleanly. SuppressTelemetry (today: version)
	// skips the span entirely per OTEL.md §4.2.
	if !desc.SuppressTelemetry {
		var spanEnd func()
		ctx, spanEnd = openCommandSpan(parentCtx, desc)
		defer spanEnd()
	}

	// Handler boundary start bookend — one DEBUG line per invocation
	// per OBSERVABILITY.md §Boundary Logging. Handlers do not emit
	// their own boundary records; keeping the calls here guarantees
	// consistent keys.
	slog.DebugContext(ctx, "quest command start", startAttrs(cfg, desc.Name)...)
	defer func() {
		slog.DebugContext(ctx, "quest command complete", completeAttrs(cfg, desc.Name, exitCode, time.Since(start))...)
	}()

	// Step 3 — role gate (elevated commands only). Runs before
	// workspace / config validation so role denial is uniform across
	// all workspace states per spec §Error precedence.
	if desc.Elevated {
		allowed := config.IsElevated(cfg.Agent.Role, cfg.Workspace.ElevatedRoles)
		telemetry.GateSpan(ctx, cfg.Agent.Role, allowed)
		if !allowed {
			slog.InfoContext(ctx, "role gate denied",
				"command", desc.Name,
				"agent.role", normalizedRole(cfg.Agent.Role),
				"required", "elevated",
			)
			return telemetry.RecordDispatchError(ctx, errors.ErrRoleDenied, stderr)
		}
	}

	// Step 4 — workspace presence + config validation. The
	// "not in a quest workspace" message is spec-specific; once a
	// workspace is present, fall back to Validate's collected errors.
	if desc.RequiresWorkspace {
		if cfg.Workspace.Root == "" {
			err := fmt.Errorf("%w: not in a quest workspace — run 'quest init --prefix PFX' first", errors.ErrUsage)
			return telemetry.RecordDispatchError(ctx, err, stderr)
		}
		if err := cfg.Validate(); err != nil {
			wrapped := fmt.Errorf("%w: %s", errors.ErrUsage, err.Error())
			return telemetry.RecordDispatchError(ctx, wrapped, stderr)
		}
	}

	// Step 5 — open store, check schema version, run any pending
	// migrations. MigrateSpan parents on parentCtx so quest.db.migrate
	// is a sibling of the command span per OTEL.md §8.8. Schema too
	// new exits ErrGeneral (exit 1) per spec §Storage — downgrade is
	// restore-from-export, not schema rollback.
	var s store.Store
	if desc.RequiresWorkspace {
		opened, err := store.Open(cfg.Workspace.DBPath)
		if err != nil {
			return telemetry.RecordDispatchError(ctx, err, stderr)
		}
		defer opened.Close()
		s = telemetry.WrapStore(opened)
		from, err := s.CurrentSchemaVersion(ctx)
		if err != nil {
			return telemetry.RecordDispatchError(ctx, err, stderr)
		}
		switch {
		case from < store.SupportedSchemaVersion:
			migCtx, end := telemetry.MigrateSpan(parentCtx, from, store.SupportedSchemaVersion)
			applied, mErr := store.Migrate(migCtx, s)
			end(applied, mErr)
			if mErr != nil {
				return telemetry.RecordDispatchError(ctx, mErr, stderr)
			}
		case from > store.SupportedSchemaVersion:
			return telemetry.RecordDispatchError(ctx, errors.NewSchemaTooNew(from, store.SupportedSchemaVersion), stderr)
		}
	}

	// Step 6 — SuppressTelemetry short-circuit (today: version). Skip
	// WrapCommand entirely so no span / operations counter fires.
	if desc.SuppressTelemetry {
		if err := desc.Handler(ctx, cfg, s, args[1:], stdin, stdout, stderr); err != nil {
			errors.EmitStderr(err, stderr)
			return errors.ExitCode(err)
		}
		return 0
	}

	// Step 7 — WrapCommand applies the §4.4 three-step error pattern
	// to the already-open command span and increments the operations
	// counter (both are Phase 12 additions; in Phase 4 WrapCommand is
	// a pass-through that simply forwards the handler error). stderr
	// + exit code emission stay here so the dispatcher owns the
	// termination path regardless of WrapCommand's future evolution.
	err := telemetry.WrapCommand(ctx, desc.Name, func(ctx context.Context) error {
		return desc.Handler(ctx, cfg, s, args[1:], stdin, stdout, stderr)
	})
	if err != nil {
		errors.EmitStderr(err, stderr)
		return errors.ExitCode(err)
	}
	return 0
}

// openCommandSpan closes over telemetry.CommandSpan so Execute does not
// name the trace.Span return type directly — the dispatcher keeps the
// OTEL import boundary clean (OTEL.md §10.1 tripwire in
// internal/telemetry/telemetry_test.go) by ending the span inside a
// func() closure the compiler fills in.
func openCommandSpan(parent context.Context, desc commandDescriptor) (context.Context, func()) {
	ctx, span := telemetry.CommandSpan(parent, desc.Name, desc.Elevated)
	return ctx, func() { span.End() }
}

// startAttrs builds the "quest command start" attribute set per
// OBSERVABILITY.md §Standard Field Names. agent.role is always
// present ("unset" when empty); dept.task.id / dept.session.id are
// omitted when empty to match the §Standard Field Names omit-if-empty
// contract.
func startAttrs(cfg config.Config, name string) []any {
	attrs := []any{
		"command", name,
		"agent.role", normalizedRole(cfg.Agent.Role),
	}
	if cfg.Agent.Task != "" {
		attrs = append(attrs, "dept.task.id", cfg.Agent.Task)
	}
	if cfg.Agent.Session != "" {
		attrs = append(attrs, "dept.session.id", cfg.Agent.Session)
	}
	return attrs
}

// completeAttrs extends startAttrs with exit_code and duration_ms.
// Duration uses the §4.3 microseconds-over-1000 math so sub-ms
// durations survive on fast commands like `quest version`.
func completeAttrs(cfg config.Config, name string, exit int, elapsed time.Duration) []any {
	attrs := startAttrs(cfg, name)
	attrs = append(attrs,
		"exit_code", exit,
		"duration_ms", float64(elapsed.Microseconds())/1000.0,
	)
	return attrs
}

func normalizedRole(role string) string {
	if role == "" {
		return "unset"
	}
	return role
}

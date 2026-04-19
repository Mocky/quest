package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/telemetry"
)

// StderrConfig configures the text handler written to os.Stderr.
// Expanding this struct lets NewStderrHandler accept future knobs
// (e.g., "no-color") without breaking Setup's call site.
type StderrConfig struct {
	Level slog.Level
}

// NewStderrHandler returns the slog handler for the human-readable
// stderr channel. It wraps slog.NewTextHandler in a trace-enrichment
// layer that adds trace_id / span_id per OBSERVABILITY.md §Correlation
// Identifiers — the wrapper pulls the IDs from
// telemetry.TraceIDsFromContext so logging never imports OTEL
// (OTEL.md §10.1).
func NewStderrHandler(cfg StderrConfig) slog.Handler {
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.Level})
	return &traceEnrichHandler{inner: base}
}

// LevelFromString parses a level string per OBSERVABILITY.md §Log
// Levels. Returns (slog.LevelInfo, false) for empty or unknown values
// so callers can distinguish "use default" from "reject the typo" —
// config.Validate relies on the second return to flag malformed
// QUEST_LOG_LEVEL / QUEST_LOG_OTEL_LEVEL values.
func LevelFromString(s string) (slog.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

// Setup composes the stderr handler with every non-nil extra into a
// fan-out and returns the resulting *slog.Logger. Callers pass the
// OTEL bridge (from telemetry.Setup) as an extra; Phase 2 callers pass
// nothing. The fan-out is immutable — once constructed, no handler can
// be added or removed.
func Setup(cfg config.LogConfig, extras ...slog.Handler) *slog.Logger {
	stderrLevel, _ := LevelFromString(cfg.Level)
	stderr := NewStderrHandler(StderrConfig{Level: stderrLevel})
	handlers := []slog.Handler{stderr}
	for _, e := range extras {
		if e != nil {
			handlers = append(handlers, e)
		}
	}
	if len(handlers) == 1 {
		return slog.New(handlers[0])
	}
	return slog.New(&fanOutHandler{handlers: handlers})
}

// traceEnrichHandler wraps the stderr text handler. On every Handle
// call it asks telemetry.TraceIDsFromContext for the active span's
// trace and span IDs and, when a span is active, adds them as slog
// attributes before delegating to the inner handler. The Phase 2
// telemetry stub returns ok=false so records simply omit the fields.
type traceEnrichHandler struct {
	inner slog.Handler
}

func (h *traceEnrichHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.inner.Enabled(ctx, lvl)
}

func (h *traceEnrichHandler) Handle(ctx context.Context, r slog.Record) error {
	if traceID, spanID, ok := telemetry.TraceIDsFromContext(ctx); ok {
		r.AddAttrs(slog.String("trace_id", traceID), slog.String("span_id", spanID))
	}
	return h.inner.Handle(ctx, r)
}

func (h *traceEnrichHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceEnrichHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *traceEnrichHandler) WithGroup(name string) slog.Handler {
	return &traceEnrichHandler{inner: h.inner.WithGroup(name)}
}

// fanOutHandler dispatches every slog.Handler method to every child.
// Level gating is per-child, not central, so the stderr handler and
// the OTEL bridge can run at different thresholds
// (QUEST_LOG_LEVEL vs QUEST_LOG_OTEL_LEVEL, OTEL.md §3.2).
type fanOutHandler struct {
	handlers []slog.Handler
}

func (h *fanOutHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, c := range h.handlers {
		if c.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

func (h *fanOutHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, c := range h.handlers {
		if !c.Enabled(ctx, r.Level) {
			continue
		}
		if err := c.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *fanOutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	children := make([]slog.Handler, len(h.handlers))
	for i, c := range h.handlers {
		children[i] = c.WithAttrs(attrs)
	}
	return &fanOutHandler{handlers: children}
}

func (h *fanOutHandler) WithGroup(name string) slog.Handler {
	children := make([]slog.Handler, len(h.handlers))
	for i, c := range h.handlers {
		children[i] = c.WithGroup(name)
	}
	return &fanOutHandler{handlers: children}
}

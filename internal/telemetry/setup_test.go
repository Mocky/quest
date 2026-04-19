package telemetry

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	levelDebug = slog.LevelDebug
	levelInfo  = slog.LevelInfo
	levelWarn  = slog.LevelWarn
)

// resetSetupOnce lets tests re-run Setup with different env states.
// Production code never resets sync.Once — the test reaches across
// `internal/telemetry`'s package boundary because tests live in the
// same package.
func resetSetupOnce() {
	setupOnce = sync.Once{}
	markDisabled()
}

type countingHandler struct {
	enabled  bool
	onHandle func()
}

func (h *countingHandler) Enabled(_ context.Context, _ slog.Level) bool { return h.enabled }
func (h *countingHandler) Handle(_ context.Context, _ slog.Record) error {
	if h.onHandle != nil {
		h.onHandle()
	}
	return nil
}
func (h *countingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(_ string) slog.Handler      { return h }

func makeRecord(level slog.Level, msg string) slog.Record {
	return slog.NewRecord(time.Now(), level, msg, 0)
}

// TestSetupDisabledNoOp confirms that without OTEL_EXPORTER_OTLP_ENDPOINT
// Setup returns a nil bridge handler, a no-op shutdown that returns nil,
// and never errors. The disabled path is the dominant case (most CLI
// invocations), so any allocation here multiplies by the invocation rate.
func TestSetupDisabledNoOp(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	t.Setenv("OTEL_SDK_DISABLED", "")

	resetSetupOnce()
	bridge, shutdown, err := Setup(context.Background(), Config{
		ServiceName:    "quest-cli",
		ServiceVersion: "test",
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if bridge != nil {
		t.Errorf("disabled bridge = %v; want nil", bridge)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
	if enabled() {
		t.Errorf("enabled() = true after disabled Setup")
	}
}

// TestSetupSDKDisabledKillSwitch covers OTEL_SDK_DISABLED=true even
// when an endpoint is configured — operators flip this for local
// debugging without unsetting their endpoint.
func TestSetupSDKDisabledKillSwitch(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://example.invalid:4318")
	t.Setenv("OTEL_SDK_DISABLED", "true")

	resetSetupOnce()
	bridge, _, err := Setup(context.Background(), Config{ServiceName: "quest-cli"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if bridge != nil {
		t.Errorf("OTEL_SDK_DISABLED bridge = %v; want nil", bridge)
	}
	if enabled() {
		t.Errorf("enabled() = true with OTEL_SDK_DISABLED")
	}
}

// TestRoleOrUnsetEmpty pins the §8.6 cross-signal join contract: an
// empty role surfaces as the literal string "unset" everywhere
// (gen_ai.agent.name, metric `role` dimension).
func TestRoleOrUnsetEmpty(t *testing.T) {
	if got := roleOrUnset(""); got != "unset" {
		t.Errorf("roleOrUnset(\"\") = %q; want %q", got, "unset")
	}
	if got := roleOrUnset("coder"); got != "coder" {
		t.Errorf("roleOrUnset(\"coder\") = %q; want %q", got, "coder")
	}
}

// TestSetIdentityCachesUnset confirms setIdentity normalizes the
// AgentRole at storage time — handlers do not re-apply roleOrUnset on
// the read side.
func TestSetIdentityCachesUnset(t *testing.T) {
	defer setIdentity("", "", "")
	setIdentity("", "task-1", "session-1")
	if identity.agentRole != "unset" {
		t.Errorf("identity.agentRole = %q; want %q", identity.agentRole, "unset")
	}
	if identity.agentTask != "task-1" {
		t.Errorf("identity.agentTask = %q; want %q", identity.agentTask, "task-1")
	}
	if identity.agentSession != "session-1" {
		t.Errorf("identity.agentSession = %q; want %q", identity.agentSession, "session-1")
	}
}

// TestExtractTraceFromConfigEmptyTraceParent confirms an empty
// traceparent short-circuits even when tracestate is set.
func TestExtractTraceFromConfigEmptyTraceParent(t *testing.T) {
	ctx := context.Background()
	got := ExtractTraceFromConfig(ctx, "", "rojo=00f067aa0ba902b7")
	if got != ctx {
		t.Errorf("ExtractTraceFromConfig with empty traceparent altered ctx")
	}
}

// TestExtractTraceFromConfigPropagates checks that a valid traceparent
// reaches the propagator and produces a context whose span context
// matches the parsed input. Setup must register the W3C propagator
// first; we call registerPropagator directly to mirror Setup's order.
func TestExtractTraceFromConfigPropagates(t *testing.T) {
	registerPropagator()
	ctx := context.Background()
	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	got := ExtractTraceFromConfig(ctx, tp, "")
	traceID, _, ok := TraceIDsFromContext(got)
	if !ok {
		t.Fatalf("TraceIDsFromContext: ok=false; want true")
	}
	if !strings.EqualFold(traceID, "0af7651916cd43dd8448eb211c80319c") {
		t.Errorf("traceID = %q; want %q", traceID, "0af7651916cd43dd8448eb211c80319c")
	}
}

// TestSetupRegistersPropagator confirms the disabled path still
// registers the W3C propagator — without it ExtractTraceFromConfig
// silently swallows TRACEPARENT, the most common quiet OTEL failure
// mode (OTEL.md §7.3).
func TestSetupRegistersPropagator(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SDK_DISABLED", "")
	resetSetupOnce()
	if _, _, err := Setup(context.Background(), Config{ServiceName: "quest-cli"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	got := ExtractTraceFromConfig(context.Background(), tp, "")
	if _, _, ok := TraceIDsFromContext(got); !ok {
		t.Errorf("propagator not registered after disabled Setup")
	}
}

// TestLevelGatedHandler confirms the bridge-side level filter honors
// QUEST_LOG_OTEL_LEVEL independently of the stderr level
// (OBSERVABILITY.md §Logger Setup).
func TestLevelGatedHandler(t *testing.T) {
	// Build a level-gated handler around a recording inner handler. We
	// intentionally skip the otelslog bridge here so the test stays
	// pure — the gating logic is what matters.
	cnt := 0
	inner := &countingHandler{onHandle: func() { cnt++ }, enabled: true}
	h := &levelGatedHandler{inner: inner, level: levelInfo}

	if h.Enabled(context.Background(), levelDebug) {
		t.Errorf("debug Enabled = true; want false at info level")
	}
	if !h.Enabled(context.Background(), levelWarn) {
		t.Errorf("warn Enabled = false; want true at info level")
	}

	rec := makeRecord(levelDebug, "should be filtered")
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle debug: %v", err)
	}
	if cnt != 0 {
		t.Errorf("inner Handle invoked for debug record; want 0, got %d", cnt)
	}
	rec = makeRecord(levelWarn, "should pass")
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle warn: %v", err)
	}
	if cnt != 1 {
		t.Errorf("inner Handle count for warn record = %d; want 1", cnt)
	}
}

package telemetry

import (
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// identity caches the post-roleOrUnset agent identity so every CommandSpan
// and RecordX call avoids repeated map lookups and guarantees that spans
// and metrics carry the same role/task/session string. setIdentity is
// called once by Setup before any command runs; after that the fields
// are read-only (OTEL.md §8.2).
var identity struct {
	agentRole    string
	agentTask    string
	agentSession string
}

// captureContent is set once by Setup from cfg.Telemetry.CaptureContent
// (OTEL.md §4.5). Read-only after Setup returns.
var captureContent bool

// enabledFlag is flipped to 1 by markEnabled when telemetry.Setup
// installs real providers. Reads are atomic so InstrumentedStore can
// short-circuit without a mutex on the hot path.
var enabledFlag atomic.Bool

// batchInterval is the export cadence for the BatchSpanProcessor and
// BatchLogRecordProcessor (OTEL.md §7.1). One second matches the spec;
// keeping it here avoids two literal call sites drifting apart.
const batchInterval = time.Second

// tracer and meter are package-level handles per OTEL.md §10.3 — every
// span / instrument allocation goes through them. The instrumentation
// scope name `dept.quest` matches the framework convention.
var tracer = otel.Tracer("dept.quest")
var meter metric.Meter = otel.Meter("dept.quest")

func setIdentity(role, task, session string) {
	identity.agentRole = roleOrUnset(role)
	identity.agentTask = task
	identity.agentSession = session
}

func setCaptureContent(b bool) { captureContent = b }

// roleOrUnset maps empty AGENT_ROLE to the literal string "unset" so span
// attributes and metric dimensions carrying role stay joinable across
// signals. Applied on both spans (gen_ai.agent.name) and metrics (role),
// per OTEL.md §8.6.
func roleOrUnset(role string) string {
	if role == "" {
		return "unset"
	}
	return role
}

// CaptureContentEnabled reports whether OTEL_GENAI_CAPTURE_CONTENT is set.
// Handlers gate content-span-event emission at the call site on this flag
// (OTEL.md §4.5).
func CaptureContentEnabled() bool { return captureContent }

func markEnabled() {
	enabledFlag.Store(true)
	tracer = otel.Tracer("dept.quest")
	meter = otel.Meter("dept.quest")
}

// markDisabled records the no-op path so InstrumentedStore can return
// the bare store unchanged (OTEL.md §8.3).
func markDisabled() { enabledFlag.Store(false) }

// enabled reports whether real OTEL providers are installed. Internal
// only — handlers do not gate on this; the no-op providers make the hot
// path cheap by design (OTEL.md §8.3).
func enabled() bool { return enabledFlag.Load() }

// nonRecording returns true when ctx carries no recording span. Used by
// recorders to skip allocation-heavy attribute construction when the
// surrounding command span is not being captured.
func nonRecording(s trace.Span) bool { return s == nil || !s.IsRecording() }

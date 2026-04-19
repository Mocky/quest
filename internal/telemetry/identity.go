package telemetry

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
// (OTEL.md §4.5). False in Phase 2 regardless of the env var until the
// real Setup lands.
func CaptureContentEnabled() bool { return captureContent }

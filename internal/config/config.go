package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mocky/quest/internal/ids"
)

// Flags carries the position-independent global flags the dispatcher
// extracts before resolving Config. Per STANDARDS.md §Flag Overrides,
// `--color` is deliberately absent.
type Flags struct {
	Format   string
	LogLevel string
}

// WorkspaceConfig captures the project-level facts. Root and DBPath are
// computed by Load; IDPrefix, ElevatedRoles, and EnforceSessionOwnership
// come from .quest/config.toml.
type WorkspaceConfig struct {
	Root                    string
	DBPath                  string
	IDPrefix                string
	ElevatedRoles           []string
	EnforceSessionOwnership bool
}

// AgentConfig holds resolved values from AGENT_* and TRACE* env vars.
// Empty string is a valid unset state — no substitution.
type AgentConfig struct {
	Role        string
	Task        string
	Session     string
	TraceParent string
	TraceState  string
}

// LogConfig pins the two logger knobs documented in OBSERVABILITY.md
// §Logger Setup.
type LogConfig struct {
	Level     string
	OTELLevel string
}

// TelemetryConfig isolates the telemetry-facing booleans Config owns.
// Standard OTEL_* variables are read by the OTEL SDK directly
// (OTEL.md §7).
type TelemetryConfig struct {
	CaptureContent bool
}

// OutputConfig controls stdout rendering for command results. Text mode
// is a human rendering, not a contract — agents read json.
type OutputConfig struct {
	Format string
}

// Config is the resolved runtime configuration. Load never returns an
// error; Validate surfaces missing or malformed fields.
type Config struct {
	Workspace WorkspaceConfig
	Agent     AgentConfig
	Log       LogConfig
	Telemetry TelemetryConfig
	Output    OutputConfig
}

// Load resolves Config from flags, environment variables, and
// .quest/config.toml. It is infallible — partial or missing
// configuration is surfaced by Validate.
func Load(flags Flags) Config {
	root := discoverWorkspace()
	file := readFile(root)

	return Config{
		Workspace: WorkspaceConfig{
			Root:                    root,
			DBPath:                  dbPath(root),
			IDPrefix:                file.IDPrefix,
			ElevatedRoles:           file.ElevatedRoles,
			EnforceSessionOwnership: file.EnforceSessionOwnership,
		},
		Agent: AgentConfig{
			Role:        os.Getenv("AGENT_ROLE"),
			Task:        os.Getenv("AGENT_TASK"),
			Session:     os.Getenv("AGENT_SESSION"),
			TraceParent: os.Getenv("TRACEPARENT"),
			TraceState:  os.Getenv("TRACESTATE"),
		},
		Log: LogConfig{
			Level:     firstNonEmpty(flags.LogLevel, os.Getenv("QUEST_LOG_LEVEL"), "warn"),
			OTELLevel: firstNonEmpty(os.Getenv("QUEST_LOG_OTEL_LEVEL"), "info"),
		},
		Telemetry: TelemetryConfig{
			CaptureContent: captureContent(),
		},
		Output: OutputConfig{
			Format: firstNonEmpty(flags.Format, "json"),
		},
	}
}

// Validate reports every configuration problem in a single error. The
// dispatcher calls it for workspace-bound commands; `quest init` and
// `quest version` skip it.
func (c Config) Validate() error {
	var errs []string
	if c.Workspace.Root == "" {
		errs = append(errs, "workspace: no .quest/ directory found in current directory or any parent")
	}
	if c.Workspace.IDPrefix == "" {
		if c.Workspace.Root != "" {
			errs = append(errs, ".quest/config.toml: id_prefix is required")
		}
	} else if perr := ids.ValidatePrefix(c.Workspace.IDPrefix); perr != nil {
		errs = append(errs, ".quest/config.toml: id_prefix: "+perr.Error())
	}
	if !validLogLevel(c.Log.Level) {
		errs = append(errs, fmt.Sprintf("QUEST_LOG_LEVEL: %q is not a valid log level", c.Log.Level))
	}
	if !validLogLevel(c.Log.OTELLevel) {
		errs = append(errs, fmt.Sprintf("QUEST_LOG_OTEL_LEVEL: %q is not a valid log level", c.Log.OTELLevel))
	}
	if f := c.Output.Format; f != "json" && f != "text" {
		errs = append(errs, fmt.Sprintf("--format: %q is not a valid output format", f))
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.New("configuration errors:\n  " + strings.Join(errs, "\n  "))
}

// IsElevated reports whether the caller may invoke elevated commands.
// Role gating is opt-in restriction: an empty role (typical of humans
// or any caller outside vigil) passes; an explicit role passes only
// when listed in elevated. A non-empty role not in elevated is the
// only state that returns false, which is what produces exit 6 at
// the dispatcher. See spec §Role Gating > Resolution order.
func IsElevated(role string, elevated []string) bool {
	if role == "" {
		return true
	}
	for _, e := range elevated {
		if e == role {
			return true
		}
	}
	return false
}

func discoverWorkspace() string {
	cwd, err := os.Getwd()
	if err != nil {
		slog.Warn("workspace discovery", "err", err.Error())
		return ""
	}
	root, err := DiscoverRoot(cwd)
	if err != nil {
		if !errors.Is(err, ErrNoWorkspace) {
			slog.Warn("workspace discovery", "err", err.Error())
		}
		return ""
	}
	return root
}

func readFile(root string) FileConfig {
	cfg, err := ReadFile(root)
	if err == nil {
		return cfg
	}
	if errors.Is(err, os.ErrNotExist) {
		return FileConfig{}
	}
	slog.Warn("read .quest/config.toml", "path", filepath.Join(root, ".quest", "config.toml"), "err", err.Error())
	return FileConfig{}
}

func dbPath(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".quest", "quest.db")
}

func captureContent() bool {
	raw, ok := os.LookupEnv("OTEL_GENAI_CAPTURE_CONTENT")
	if !ok || raw == "" {
		return false
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("invalid OTEL_GENAI_CAPTURE_CONTENT", "value", raw)
		return false
	}
	return b
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func validLogLevel(s string) bool {
	switch strings.ToLower(s) {
	case "debug", "info", "warn", "warning", "error":
		return true
	}
	return false
}

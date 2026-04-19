package config

import "os"

type Flags struct {
	Format   string
	LogLevel string
}

type LogConfig struct {
	Level string
}

type AgentConfig struct {
	Role        string
	Task        string
	Session     string
	TraceParent string
	TraceState  string
}

type TelemetryConfig struct {
	CaptureContent bool
}

type Config struct {
	Format    string
	Log       LogConfig
	Agent     AgentConfig
	Telemetry TelemetryConfig
}

func Load(flags Flags) Config {
	format := flags.Format
	if format == "" {
		format = "json"
	}
	level := flags.LogLevel
	if level == "" {
		level = "info"
	}
	return Config{
		Format: format,
		Log:    LogConfig{Level: level},
		Agent: AgentConfig{
			Role:        os.Getenv("AGENT_ROLE"),
			Task:        os.Getenv("AGENT_TASK"),
			Session:     os.Getenv("AGENT_SESSION"),
			TraceParent: os.Getenv("TRACEPARENT"),
			TraceState:  os.Getenv("TRACESTATE"),
		},
		Telemetry: TelemetryConfig{
			CaptureContent: os.Getenv("OTEL_GENAI_CAPTURE_CONTENT") == "true",
		},
	}
}

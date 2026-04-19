package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AGENT_ROLE", "AGENT_TASK", "AGENT_SESSION",
		"TRACEPARENT", "TRACESTATE",
		"QUEST_LOG_LEVEL", "QUEST_LOG_OTEL_LEVEL",
		"OTEL_GENAI_CAPTURE_CONTENT",
	} {
		t.Setenv(k, "")
	}
}

func workspaceWithPrefix(t *testing.T, prefix string) string {
	t.Helper()
	root := t.TempDir()
	writeConfig(t, root, "id_prefix = \""+prefix+"\"\nelevated_roles = [\"planner\"]\n")
	return root
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	root := workspaceWithPrefix(t, "proj")
	chdir(t, root)

	cfg := Load(Flags{})

	if cfg.Workspace.Root != root {
		t.Errorf("Root = %q, want %q", cfg.Workspace.Root, root)
	}
	if got, want := cfg.Workspace.DBPath, filepath.Join(root, ".quest", "quest.db"); got != want {
		t.Errorf("DBPath = %q, want %q", got, want)
	}
	if cfg.Workspace.IDPrefix != "proj" {
		t.Errorf("IDPrefix = %q, want proj", cfg.Workspace.IDPrefix)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("Log.Level default = %q, want warn", cfg.Log.Level)
	}
	if cfg.Log.OTELLevel != "info" {
		t.Errorf("Log.OTELLevel default = %q, want info", cfg.Log.OTELLevel)
	}
	if cfg.Output.Format != "json" {
		t.Errorf("Output.Format default = %q, want json", cfg.Output.Format)
	}
	if cfg.Telemetry.CaptureContent {
		t.Errorf("CaptureContent = true, want false by default")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestLoadAgentEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENT_ROLE", "planner")
	t.Setenv("AGENT_TASK", "proj-01")
	t.Setenv("AGENT_SESSION", "sess-xyz")
	t.Setenv("TRACEPARENT", "00-abc-def-01")
	t.Setenv("TRACESTATE", "vendor=abc")

	cfg := Load(Flags{})

	if cfg.Agent.Role != "planner" {
		t.Errorf("Agent.Role = %q, want planner", cfg.Agent.Role)
	}
	if cfg.Agent.Task != "proj-01" {
		t.Errorf("Agent.Task = %q, want proj-01", cfg.Agent.Task)
	}
	if cfg.Agent.Session != "sess-xyz" {
		t.Errorf("Agent.Session = %q, want sess-xyz", cfg.Agent.Session)
	}
	if cfg.Agent.TraceParent != "00-abc-def-01" {
		t.Errorf("Agent.TraceParent = %q", cfg.Agent.TraceParent)
	}
	if cfg.Agent.TraceState != "vendor=abc" {
		t.Errorf("Agent.TraceState = %q", cfg.Agent.TraceState)
	}
}

func TestLogLevelPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		env     string
		wantLvl string
	}{
		{"default", "", "", "warn"},
		{"env only", "", "debug", "debug"},
		{"flag only", "error", "", "error"},
		{"flag beats env", "info", "debug", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			if tt.env != "" {
				t.Setenv("QUEST_LOG_LEVEL", tt.env)
			}
			cfg := Load(Flags{LogLevel: tt.flag})
			if cfg.Log.Level != tt.wantLvl {
				t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, tt.wantLvl)
			}
		})
	}
}

func TestFormatPrecedence(t *testing.T) {
	clearEnv(t)

	t.Run("default json", func(t *testing.T) {
		cfg := Load(Flags{})
		if cfg.Output.Format != "json" {
			t.Errorf("Format = %q, want json", cfg.Output.Format)
		}
	})

	t.Run("flag override", func(t *testing.T) {
		cfg := Load(Flags{Format: "text"})
		if cfg.Output.Format != "text" {
			t.Errorf("Format = %q, want text", cfg.Output.Format)
		}
	})
}

func TestLoadWorkspaceless(t *testing.T) {
	clearEnv(t)
	chdir(t, t.TempDir())

	cfg := Load(Flags{})

	if cfg.Workspace.Root != "" {
		t.Errorf("Root = %q, want empty", cfg.Workspace.Root)
	}
	if cfg.Workspace.DBPath != "" {
		t.Errorf("DBPath = %q, want empty", cfg.Workspace.DBPath)
	}
	if cfg.Workspace.IDPrefix != "" {
		t.Errorf("IDPrefix = %q, want empty", cfg.Workspace.IDPrefix)
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "workspace:") {
		t.Errorf("message missing workspace reference: %s", msg)
	}
}

func TestValidateCollectsErrors(t *testing.T) {
	cfg := Config{
		Workspace: WorkspaceConfig{Root: "/tmp/fake", IDPrefix: "Bad-Prefix"},
		Log:       LogConfig{Level: "spammy", OTELLevel: "ghost"},
		Output:    OutputConfig{Format: "yaml"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	msg := err.Error()
	for _, want := range []string{
		".quest/config.toml: id_prefix",
		"QUEST_LOG_LEVEL",
		"QUEST_LOG_OTEL_LEVEL",
		"--format",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\nfull:\n%s", want, msg)
		}
	}
}

func TestValidateMissingIDPrefix(t *testing.T) {
	cfg := Config{
		Workspace: WorkspaceConfig{Root: "/tmp/fake"},
		Log:       LogConfig{Level: "warn", OTELLevel: "info"},
		Output:    OutputConfig{Format: "json"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	if !strings.Contains(err.Error(), "id_prefix is required") {
		t.Errorf("message missing id_prefix guidance: %s", err.Error())
	}
}

func TestValidateGoodConfig(t *testing.T) {
	cfg := Config{
		Workspace: WorkspaceConfig{Root: "/tmp/fake", IDPrefix: "proj"},
		Log:       LogConfig{Level: "warn", OTELLevel: "info"},
		Output:    OutputConfig{Format: "json"},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestIsElevated(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		elevated []string
		want     bool
	}{
		{"empty role, empty list", "", nil, false},
		{"empty role, with list", "", []string{"planner"}, false},
		{"role in list", "planner", []string{"planner"}, true},
		{"role in multi-list", "lead", []string{"planner", "lead", "admin"}, true},
		{"role absent", "coder", []string{"planner"}, false},
		{"role, empty list", "planner", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsElevated(tt.role, tt.elevated); got != tt.want {
				t.Errorf("IsElevated(%q, %v) = %v, want %v",
					tt.role, tt.elevated, got, tt.want)
			}
		})
	}
}

func TestCaptureContentParsing(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		want     bool
		wantWarn bool
	}{
		{"unset", "", false, false},
		{"true", "true", true, false},
		{"1", "1", true, false},
		{"false", "false", false, false},
		{"0", "0", false, false},
		{"True", "True", true, false},
		{"yes invalid", "yes", false, true},
		{"on invalid", "on", false, true},
		{"1.0 invalid", "1.0", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			if tt.value != "" {
				t.Setenv("OTEL_GENAI_CAPTURE_CONTENT", tt.value)
			}
			rec := captureSlog(t)
			cfg := Load(Flags{})
			if cfg.Telemetry.CaptureContent != tt.want {
				t.Errorf("CaptureContent = %v, want %v", cfg.Telemetry.CaptureContent, tt.want)
			}
			warnCount := rec.count("invalid OTEL_GENAI_CAPTURE_CONTENT")
			if tt.wantWarn {
				if warnCount != 1 {
					t.Errorf("warn count = %d, want 1", warnCount)
				}
			} else if warnCount != 0 {
				t.Errorf("unexpected warn for %q (count=%d)", tt.value, warnCount)
			}
		})
	}
}

func TestLoadWarnsOnMalformedTOML(t *testing.T) {
	clearEnv(t)
	root := t.TempDir()
	writeConfig(t, root, "id_prefix = \x00broken\n")
	chdir(t, root)

	rec := captureSlog(t)
	cfg := Load(Flags{})

	if cfg.Workspace.Root != root {
		t.Errorf("Root = %q, want %q", cfg.Workspace.Root, root)
	}
	if cfg.Workspace.IDPrefix != "" {
		t.Errorf("IDPrefix = %q, want empty after parse failure", cfg.Workspace.IDPrefix)
	}
	if !rec.has("read .quest/config.toml") {
		t.Errorf("no WARN for malformed TOML; records: %v", rec.records())
	}
}

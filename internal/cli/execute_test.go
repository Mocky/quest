package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/mocky/quest/internal/buildinfo"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// baseCfg builds a Config that is just valid enough for the role and
// workspace checks to exercise. Individual tests override the fields
// they care about.
func baseCfg() config.Config {
	return config.Config{
		Workspace: config.WorkspaceConfig{Root: "", DBPath: "", IDPrefix: "", ElevatedRoles: []string{"planner"}},
		Agent:     config.AgentConfig{Role: ""},
		Log:       config.LogConfig{Level: "warn", OTELLevel: "info"},
		Output:    config.OutputConfig{Format: "json"},
	}
}

func runExecute(args []string, cfg config.Config) (exit int, stdout, stderr string) {
	var outBuf, errBuf bytes.Buffer
	exit = Execute(context.Background(), cfg, args, strings.NewReader(""), &outBuf, &errBuf)
	return exit, outBuf.String(), errBuf.String()
}

func TestExecuteNoArgsPrintsBanner(t *testing.T) {
	exit, stdout, stderr := runExecute(nil, baseCfg())
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if !strings.Contains(stdout, "Usage: quest <command>") {
		t.Errorf("stdout missing usage banner: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr not empty: %q", stderr)
	}
}

func TestExecuteHelpFlagPrintsBanner(t *testing.T) {
	exit, stdout, _ := runExecute([]string{"--help"}, baseCfg())
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if !strings.Contains(stdout, "Usage: quest") {
		t.Errorf("stdout missing banner: %q", stdout)
	}
}

func TestExecuteVersion(t *testing.T) {
	exit, stdout, _ := runExecute([]string{"version"}, baseCfg())
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", err, stdout)
	}
	if got.Version != buildinfo.Version {
		t.Errorf("version=%q, want %q", got.Version, buildinfo.Version)
	}
}

func TestExecuteVersionText(t *testing.T) {
	cfg := baseCfg()
	cfg.Output.Format = "text"
	exit, stdout, _ := runExecute([]string{"version"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	line := strings.TrimRight(stdout, "\n")
	if line == "" || strings.Contains(line, "{") {
		t.Errorf("text version looks wrong: %q", stdout)
	}
}

// Unknown command: exit 2, stderr banner includes the usage_error
// prefix and the unknown-token body.
func TestExecuteUnknownCommand(t *testing.T) {
	exit, _, stderr := runExecute([]string{"stauts"}, baseCfg())
	if exit != 2 {
		t.Fatalf("exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr, "quest: usage_error:") {
		t.Errorf("stderr missing usage_error prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "unknown command 'stauts'") {
		t.Errorf("stderr missing unknown-command body: %q", stderr)
	}
}

// A close typo produces a "did you mean" hint via cli.Suggest. Use a
// planner role so `accept` is in the role-filtered banner.
func TestExecuteUnknownCommandDidYouMean(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "planner"
	_, _, stderr := runExecute([]string{"accpt"}, cfg)
	if !strings.Contains(stderr, "did you mean 'accept'") {
		t.Errorf("stderr missing 'did you mean accept' hint: %q", stderr)
	}
}

// Worker role invoking `quest create` → exit 6 with role_denied class.
// Role gate runs before workspace validation, so the worker gets the
// same denial whether or not a workspace is present (spec §Error
// precedence).
func TestExecuteRoleGateDeniesWorker(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "worker"
	cfg.Workspace.Root = "" // no workspace; role gate should still fire first
	exit, _, stderr := runExecute([]string{"create"}, cfg)
	if exit != 6 {
		t.Fatalf("exit = %d, want 6 (role_denied)", exit)
	}
	if !strings.Contains(stderr, "quest: role_denied:") {
		t.Errorf("stderr missing role_denied prefix: %q", stderr)
	}
}

// An elevated role calling a planner command must pass the role gate,
// then hit the workspace check when Root is empty.
func TestExecuteElevatedRoleReachesWorkspaceCheck(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "planner"
	cfg.Workspace.Root = ""
	exit, _, stderr := runExecute([]string{"create"}, cfg)
	if exit != 2 {
		t.Fatalf("exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr, "not in a quest workspace") {
		t.Errorf("stderr missing workspace-missing message: %q", stderr)
	}
}

// A worker running a non-elevated, workspace-bound command outside a
// workspace hits exit 2 with the workspace-missing message.
func TestExecuteWorkerNoWorkspace(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "worker"
	cfg.Workspace.Root = ""
	exit, _, stderr := runExecute([]string{"show"}, cfg)
	if exit != 2 {
		t.Fatalf("exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr, "not in a quest workspace") {
		t.Errorf("stderr missing workspace-missing message: %q", stderr)
	}
}

// quest version still works when there is no workspace — SuppressTelemetry
// and RequiresWorkspace=false combine to skip the gate and store open.
func TestExecuteVersionWorksWithoutWorkspace(t *testing.T) {
	cfg := baseCfg()
	cfg.Workspace.Root = ""
	exit, stdout, stderr := runExecute([]string{"version"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if stderr != "" {
		t.Errorf("stderr not empty: %q", stderr)
	}
	if !strings.Contains(stdout, `"version"`) {
		t.Errorf("stdout missing version: %q", stdout)
	}
}

// Unknown commands respect the role-filter banner: workers should not
// see `create` listed as a valid option.
func TestExecuteUnknownCommandWorkerBannerExcludesElevated(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "worker"
	_, _, stderr := runExecute([]string{"xxx"}, cfg)
	if strings.Contains(stderr, "create") {
		t.Errorf("worker banner leaks elevated command: %q", stderr)
	}
	if !strings.Contains(stderr, "show") {
		t.Errorf("worker banner missing worker command: %q", stderr)
	}
}

// A planner seeing the unknown-command banner gets the full inventory.
func TestExecuteUnknownCommandPlannerBannerFull(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "planner"
	_, _, stderr := runExecute([]string{"xxx"}, cfg)
	if !strings.Contains(stderr, "create") {
		t.Errorf("planner banner missing elevated command: %q", stderr)
	}
	if !strings.Contains(stderr, "export") {
		t.Errorf("planner banner missing export: %q", stderr)
	}
}

// Panic recovery: a handler that panics is translated into exit 1
// with the general_failure class on stderr and an ERROR slog record.
func TestExecutePanicRecovery(t *testing.T) {
	// Install a handler that panics, replacing the version descriptor
	// for the duration of the test.
	orig := descriptors
	descriptors = []commandDescriptor{{
		Name: "boom",
		Handler: func(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			panic("kaboom")
		},
		SuppressTelemetry: true,
	}}
	rebuildDescriptorIndex()
	defer func() {
		descriptors = orig
		rebuildDescriptorIndex()
	}()

	exit, _, stderr := runExecute([]string{"boom"}, baseCfg())
	if exit != 1 {
		t.Fatalf("exit = %d, want 1 (general_failure)", exit)
	}
	if !strings.Contains(stderr, "quest: general_failure:") {
		t.Errorf("stderr missing general_failure prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "panic: kaboom") {
		t.Errorf("stderr missing panic payload: %q", stderr)
	}
}

// rebuildDescriptorIndex is used by TestExecutePanicRecovery to
// refresh the lookup map after mutating the descriptors slice. Tests
// live in the same package so this helper can reach package state.
func rebuildDescriptorIndex() {
	descriptorIndex = make(map[string]commandDescriptor, len(descriptors))
	for _, d := range descriptors {
		descriptorIndex[d.Name] = d
	}
}

// Sanity: exit code mapping on a handler-returned error flows through
// EmitStderr + ExitCode correctly.
func TestExecuteHandlerReturnedError(t *testing.T) {
	orig := descriptors
	descriptors = []commandDescriptor{{
		Name: "fail",
		Handler: func(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return errors.ErrConflict
		},
		SuppressTelemetry: true,
	}}
	rebuildDescriptorIndex()
	defer func() {
		descriptors = orig
		rebuildDescriptorIndex()
	}()

	exit, _, stderr := runExecute([]string{"fail"}, baseCfg())
	if exit != 5 {
		t.Fatalf("exit = %d, want 5 (conflict)", exit)
	}
	if !strings.Contains(stderr, "quest: conflict:") {
		t.Errorf("stderr missing conflict prefix: %q", stderr)
	}
}

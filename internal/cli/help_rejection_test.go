package cli

import (
	"strings"
	"testing"
)

// TestHelpFlagRejected pins the obsolete-form contract from the
// 2026-05-06 grove decision: `--help` or `-h` in any position exits 2
// with the two-line "did you mean: quest help <cmd>" redirect. The
// shape (error line, then `Did you mean:` line) matches lore's
// typo-suggestion shape so an agent that already learned to read one
// recognizes the other.
func TestHelpFlagRejected(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantToken   string
		wantSuggest string
	}{
		{"bare --help", []string{"--help"}, "--help", "quest help"},
		{"bare -h", []string{"-h"}, "-h", "quest help"},
		{"--help before cmd", []string{"--help", "show"}, "--help", "quest help show"},
		{"-h before cmd", []string{"-h", "show"}, "-h", "quest help show"},
		{"--help after cmd", []string{"show", "--help"}, "--help", "quest help show"},
		{"-h after cmd", []string{"show", "-h"}, "-h", "quest help show"},
		{"--help after id", []string{"show", "proj-01", "--help"}, "--help", "quest help show"},
		{"--help with sibling flags", []string{"list", "--ready", "--help"}, "--help", "quest help list"},
		{"--help on elevated cmd", []string{"cancel", "qst-01", "--help"}, "--help", "quest help cancel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exit, _, stderr := runExecute(tc.args, baseCfg())
			if exit != 2 {
				t.Fatalf("exit = %d, want 2 (usage_error); stderr=%q", exit, stderr)
			}
			if !strings.Contains(stderr, "unknown flag: "+tc.wantToken) {
				t.Errorf("stderr missing %q; got %q", "unknown flag: "+tc.wantToken, stderr)
			}
			if !strings.Contains(stderr, "Did you mean: "+tc.wantSuggest) {
				t.Errorf("stderr missing %q; got %q", "Did you mean: "+tc.wantSuggest, stderr)
			}
		})
	}
}

// TestHelpDoesNotEnterRoleGate pins the load-bearing property from
// the plan: `quest help <cmd>` short-circuits before role enforcement,
// span open, and workspace lookup. Concrete probe — a worker (a role
// that gets exit 6 on bare `quest cancel`) invoking `quest help cancel`
// returns exit 0 with the cancel help block on stdout, with no
// workspace required.
func TestHelpDoesNotEnterRoleGate(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "worker"
	cfg.Workspace.Root = "" // also: no workspace
	exit, stdout, stderr := runExecute([]string{"help", "cancel"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", exit, stderr)
	}
	if !strings.Contains(stdout, "Usage: quest cancel") {
		t.Errorf("stdout missing cancel synopsis; got:\n%s", stdout)
	}
	// Cancel help mentions --reason and -r; both are part of cancel's
	// public surface and prove we rendered the right block.
	if !strings.Contains(stdout, "--reason") {
		t.Errorf("stdout missing --reason from cancel help; got:\n%s", stdout)
	}
}

// TestHelpForEveryCommandRendersToStdout sweeps every descriptor row
// that has a HelpFlagSet (every command except `help` itself) through
// the dispatcher. Each `quest help <cmd>` invocation must exit 0 with
// the cmd's synopsis on stdout. Catches future routing regressions
// where a help target stops resolving end-to-end.
func TestHelpForEveryCommandRendersToStdout(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "planner"
	for _, d := range descriptors {
		if d.HelpFlagSet == nil {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			exit, stdout, stderr := runExecute([]string{"help", d.Name}, cfg)
			if exit != 0 {
				t.Fatalf("exit = %d; stderr=%q", exit, stderr)
			}
			want := "Usage: quest " + d.Name
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, stdout)
			}
		})
	}
}

// TestHelpUnknownTargetRedirects pins the failure shape for
// `quest help <bogus>`: exit 2 with a redirect that reuses the typo
// machinery. `quest help shw` should suggest `show`.
func TestHelpUnknownTargetRedirects(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "planner"
	exit, _, stderr := runExecute([]string{"help", "shw"}, cfg)
	if exit != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%q", exit, stderr)
	}
	if !strings.Contains(stderr, "no help available for 'shw'") {
		t.Errorf("stderr missing 'no help available' body; got %q", stderr)
	}
	if !strings.Contains(stderr, "did you mean 'show'") {
		t.Errorf("stderr missing 'did you mean show' suggestion; got %q", stderr)
	}
}

// TestHelpNoArgPrintsBanner pins that `quest help` (no target)
// produces the same role-filtered banner as `quest` (no args).
func TestHelpNoArgPrintsBanner(t *testing.T) {
	cfg := baseCfg()
	cfg.Agent.Role = "planner"
	exit, stdout, _ := runExecute([]string{"help"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if !strings.Contains(stdout, "Usage: quest <command>") {
		t.Errorf("stdout missing banner; got %q", stdout)
	}
	// Planner banner includes elevated commands like create.
	if !strings.Contains(stdout, "create") {
		t.Errorf("planner banner missing create; got %q", stdout)
	}
	// Banner must list `help` itself so agents discover the canonical
	// form on first probe.
	if !strings.Contains(stdout, "help") {
		t.Errorf("banner missing help command; got %q", stdout)
	}
}

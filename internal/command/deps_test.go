//go:build integration

package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

func runDeps(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Deps(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// TestDepsHappyPath: a task with two dependency edges emits both as
// objects with id/title/status/link_type — status denormalized from
// the target row, relationship primitive (`link_type`) from the edge.
func TestDepsHappyPath(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Upstream-1", "", "completed")
	seedTaskWithStatus(t, s, "proj-a2", "Upstream-2", "", "accepted")
	seedTaskWithStatus(t, s, "proj-a3", "Downstream", "", "open")
	seedDep(t, s, "proj-a3", "proj-a1", "blocked-by")
	seedDep(t, s, "proj-a3", "proj-a2", "blocked-by")

	err, stdout, _ := runDeps(t, s, plannerCfg(), []string{"proj-a3"})
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	var got []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		LinkType string `json:"link_type"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if len(got) != 2 {
		t.Fatalf("got %d deps, want 2; raw=%q", len(got), stdout)
	}
	want := map[string]struct{ linkType, title, status string }{
		"proj-a1": {"blocked-by", "Upstream-1", "completed"},
		"proj-a2": {"blocked-by", "Upstream-2", "accepted"},
	}
	for _, d := range got {
		w, ok := want[d.ID]
		if !ok {
			t.Errorf("unexpected dep id %q", d.ID)
			continue
		}
		if d.LinkType != w.linkType || d.Title != w.title || d.Status != w.status {
			t.Errorf("dep %s = %+v, want {link_type=%s, title=%s, status=%s}",
				d.ID, d, w.linkType, w.title, w.status)
		}
	}
}

// TestDepsZeroDeps: a task with no outgoing edges emits [].
func TestDepsZeroDeps(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Lonely", "", "open")

	err, stdout, _ := runDeps(t, s, plannerCfg(), []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want []", stdout)
	}
}

// TestDepsNotFound: a missing task returns ErrNotFound (exit 3).
func TestDepsNotFound(t *testing.T) {
	s, _ := testStore(t)
	err, out, _ := runDeps(t, s, plannerCfg(), []string{"proj-nope"})
	if err == nil {
		t.Fatalf("got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
	if out != "" {
		t.Errorf("stdout not empty on not-found: %q", out)
	}
}

// TestDepsMissingID: no positional task ID — spec requires explicit
// ID. Must return ErrUsage (exit 2).
func TestDepsMissingID(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runDeps(t, s, plannerCfg(), nil)
	if err == nil {
		t.Fatalf("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "task ID required") {
		t.Errorf("err = %q, want 'task ID required'", err.Error())
	}
}

// TestDepsDoesNotDefaultToAgentTask: AGENT_TASK is identity/telemetry
// metadata, never a CLI default — deps requires an explicit ID even
// when AGENT_TASK is set in cfg.Agent.Task.
func TestDepsDoesNotDefaultToAgentTask(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	cfg := plannerCfg()
	cfg.Agent.Task = "proj-a1"
	err, _, _ := runDeps(t, s, cfg, nil)
	if err == nil {
		t.Fatalf("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestDepsTextFormat emits a table with header row and one data row
// per dependency.
func TestDepsTextFormat(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Upstream", "", "completed")
	seedTaskWithStatus(t, s, "proj-a2", "Downstream", "", "open")
	seedDep(t, s, "proj-a2", "proj-a1", "blocked-by")

	cfg := plannerCfg()
	cfg.Output.Text = true
	err, stdout, _ := runDeps(t, s, cfg, []string{"proj-a2"})
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if !strings.Contains(stdout, "TARGET") || !strings.Contains(stdout, "TYPE") ||
		!strings.Contains(stdout, "STATUS") || !strings.Contains(stdout, "TITLE") {
		t.Errorf("text header missing: %q", stdout)
	}
	if !strings.Contains(stdout, "proj-a1") || !strings.Contains(stdout, "blocked-by") {
		t.Errorf("text row missing: %q", stdout)
	}
}

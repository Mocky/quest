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

func runGraph(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Graph(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// graphNodeT mirrors the JSON shape for test unmarshal — pointer
// Tier/Role so we can distinguish null from empty string.
type graphNodeT struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Type     string   `json:"type"`
	Status   string   `json:"status"`
	Tier     *string  `json:"tier"`
	Role     *string  `json:"role"`
	Children []string `json:"children"`
}

type graphEdgeT struct {
	Task         string `json:"task"`
	LinkType     string `json:"link_type"`
	Target       string `json:"target"`
	TargetStatus string `json:"target_status"`
}

type graphRespT struct {
	Nodes []graphNodeT `json:"nodes"`
	Edges []graphEdgeT `json:"edges"`
}

func parseGraph(t *testing.T, stdout string) graphRespT {
	t.Helper()
	var g graphRespT
	if err := json.Unmarshal([]byte(stdout), &g); err != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", err, stdout)
	}
	return g
}

// TestGraphMissingID: quest graph with no positional task ID returns
// ErrUsage.
func TestGraphMissingID(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runGraph(t, s, plannerCfg(), nil)
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestGraphDoesNotDefaultToAgentTask: AGENT_TASK is identity/telemetry
// metadata, never a CLI default — graph requires an explicit ID even
// when AGENT_TASK is set.
func TestGraphDoesNotDefaultToAgentTask(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")

	cfg := plannerCfg()
	cfg.Agent.Task = "proj-a1"
	err, _, _ := runGraph(t, s, cfg, nil)
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestGraphNotFound: unknown ID returns ErrNotFound.
func TestGraphNotFound(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runGraph(t, s, plannerCfg(), []string{"proj-nope"})
	if err == nil {
		t.Fatal("got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestGraphRootAtEpicFullTree: the spec's main example — a parent
// with three children and two blocked-by edges under the third child.
func TestGraphRootAtEpicFullTree(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Auth module", "", "open", "coder", "T3", "")
	seedListTask(t, s, "proj-a1.1", "JWT validation", "proj-a1", "completed", "coder", "T2", "")
	seedListTask(t, s, "proj-a1.2", "Session store", "proj-a1", "accepted", "coder", "T2", "")
	seedListTask(t, s, "proj-a1.3", "Auth middleware", "proj-a1", "open", "coder", "T3", "")
	seedDep(t, s, "proj-a1.3", "proj-a1.1", "blocked-by")
	seedDep(t, s, "proj-a1.3", "proj-a1.2", "blocked-by")

	err, stdout, _ := runGraph(t, s, plannerCfg(), []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	g := parseGraph(t, stdout)
	if len(g.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4; raw=%q", len(g.Nodes), stdout)
	}
	if g.Nodes[0].ID != "proj-a1" {
		t.Errorf("nodes[0] = %q, want proj-a1 (root first)", g.Nodes[0].ID)
	}
	rootChildren := g.Nodes[0].Children
	if len(rootChildren) != 3 {
		t.Errorf("root.children = %v, want 3 entries", rootChildren)
	}

	// Edges: two blocked-by on proj-a1.3, targets inside the subtree.
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(g.Edges))
	}
	for _, e := range g.Edges {
		if e.Task != "proj-a1.3" {
			t.Errorf("edge.task = %q, want proj-a1.3", e.Task)
		}
		if e.LinkType != "blocked-by" {
			t.Errorf("edge.link_type = %q, want blocked-by", e.LinkType)
		}
	}
}

// TestGraphRootAtLeafWithExternals: rooting at a leaf whose deps
// point at siblings marks those siblings as external nodes — they
// appear in nodes but NOT as children of the leaf and NOT with their
// own children expanded.
func TestGraphRootAtLeafWithExternals(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Auth module", "", "open", "coder", "T3", "")
	seedListTask(t, s, "proj-a1.1", "JWT validation", "proj-a1", "completed", "coder", "T2", "")
	seedListTask(t, s, "proj-a1.2", "Session store", "proj-a1", "accepted", "coder", "T2", "")
	seedListTask(t, s, "proj-a1.3", "Auth middleware", "proj-a1", "open", "coder", "T3", "")
	seedDep(t, s, "proj-a1.3", "proj-a1.1", "blocked-by")
	seedDep(t, s, "proj-a1.3", "proj-a1.2", "blocked-by")

	err, stdout, _ := runGraph(t, s, plannerCfg(), []string{"proj-a1.3"})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	g := parseGraph(t, stdout)
	if len(g.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3 (root + 2 externals)", len(g.Nodes))
	}
	if g.Nodes[0].ID != "proj-a1.3" {
		t.Errorf("nodes[0] = %q, want proj-a1.3", g.Nodes[0].ID)
	}
	if len(g.Nodes[0].Children) != 0 {
		t.Errorf("root.children = %v, want []", g.Nodes[0].Children)
	}
	// External nodes appear with children: [] and full metadata.
	for _, n := range g.Nodes[1:] {
		if len(n.Children) != 0 {
			t.Errorf("external %q children = %v, want []", n.ID, n.Children)
		}
		if n.Title == "" {
			t.Errorf("external %q missing title", n.ID)
		}
	}
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(g.Edges))
	}
}

// TestGraphLeafNoDeps: rooting at a leaf with no outgoing edges
// returns just the leaf and no edges.
func TestGraphLeafNoDeps(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")

	err, stdout, _ := runGraph(t, s, plannerCfg(), []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	g := parseGraph(t, stdout)
	if len(g.Nodes) != 1 || g.Nodes[0].ID != "proj-a1" {
		t.Errorf("nodes = %+v, want [proj-a1]", g.Nodes)
	}
	if len(g.Nodes[0].Children) != 0 {
		t.Errorf("root.children = %v, want []", g.Nodes[0].Children)
	}
	if len(g.Edges) != 0 {
		t.Errorf("edges = %+v, want []", g.Edges)
	}
	// Strict JSON: edges is [] (not null, not missing).
	if !strings.Contains(stdout, `"edges":[]`) {
		t.Errorf("stdout missing empty edges array: %q", stdout)
	}
}

// TestGraphCrossProjectExternal: a dep edge pointing at a task with
// a different project prefix is external at any root.
func TestGraphCrossProjectExternal(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-31", "Crash report", "", "completed", "", "", "bug")
	seedListTask(t, s, "proj-a1", "Fix follow-up", "", "open", "", "", "task")
	seedDep(t, s, "proj-a1", "proj-31", "caused-by")

	err, stdout, _ := runGraph(t, s, plannerCfg(), []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	g := parseGraph(t, stdout)
	if len(g.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(g.Nodes))
	}
	var ext *graphNodeT
	for i := range g.Nodes {
		if g.Nodes[i].ID == "proj-31" {
			ext = &g.Nodes[i]
		}
	}
	if ext == nil {
		t.Fatalf("proj-31 not in nodes: %+v", g.Nodes)
	}
	if ext.Title != "Crash report" || ext.Type != "bug" || ext.Status != "completed" {
		t.Errorf("external node = %+v, want crash report/bug/completed", ext)
	}
	if len(g.Edges) != 1 || g.Edges[0].LinkType != "caused-by" {
		t.Errorf("edges = %+v, want one caused-by", g.Edges)
	}
}

// TestGraphTextFormat pins the indented tree + dep-edge shape.
// Every task reference uses the canonical `{id} [{status}] (bug?)
// {title}` cluster, unified with `quest show --text`.
func TestGraphTextFormat(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Auth module", "", "open", "", "", "")
	seedListTask(t, s, "proj-a1.1", "JWT", "proj-a1", "completed", "", "", "")
	seedListTask(t, s, "proj-a1.2", "Middleware", "proj-a1", "open", "", "", "")
	seedDep(t, s, "proj-a1.2", "proj-a1.1", "blocked-by")

	cfg := plannerCfg()
	cfg.Output.Text = true
	err, stdout, _ := runGraph(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if !strings.Contains(stdout, "proj-a1 [open] Auth module") {
		t.Errorf("missing root line: %q", stdout)
	}
	if !strings.Contains(stdout, "  proj-a1.1 [completed] JWT") {
		t.Errorf("missing child line: %q", stdout)
	}
	if !strings.Contains(stdout, "  proj-a1.2 [open] Middleware") {
		t.Errorf("missing second-child line: %q", stdout)
	}
	if !strings.Contains(stdout, "    blocked-by  proj-a1.1 [completed] JWT") {
		t.Errorf("missing dep edge under proj-a1.2: %q", stdout)
	}
}

// TestGraphTextFormatBugMarker pins the `(bug)` marker on both node
// lines and edge target references when the target's type is bug.
// Mirror of the show --text marker check, covering the graph
// surface so a regression in either renderer fails independently.
func TestGraphTextFormatBugMarker(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Fix auth bug", "", "open", "", "", "bug")
	seedListTask(t, s, "proj-31", "Crash report", "", "completed", "", "", "bug")
	seedDep(t, s, "proj-a1", "proj-31", "caused-by")

	cfg := plannerCfg()
	cfg.Output.Text = true
	err, stdout, _ := runGraph(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	// Root bug shows (bug) marker.
	if !strings.Contains(stdout, "proj-a1 [open] (bug) Fix auth bug") {
		t.Errorf("missing bug marker on root node: %q", stdout)
	}
	// Edge target (external bug) shows (bug) marker too.
	if !strings.Contains(stdout, "  caused-by  proj-31 [completed] (bug) Crash report") {
		t.Errorf("missing bug marker on edge target: %q", stdout)
	}
}

// TestGraphRejectsUnexpectedPositional: a second positional errors.
func TestGraphRejectsUnexpectedPositional(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runGraph(t, s, plannerCfg(), []string{"proj-a1", "proj-a2"})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

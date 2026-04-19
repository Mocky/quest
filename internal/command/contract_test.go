//go:build integration

package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/testutil"
)

// handlerFn is the local handler signature alias — internal/cli owns
// the public Handler type, but the contract suite only needs the
// function literal so we avoid a cli import here.
type handlerFn func(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error

// mustChdir cd's into dir and returns a cleanup that restores the
// previous directory. Used by Init contract tests that exercise the
// CWD-discovery path.
func mustChdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	return func() { _ = os.Chdir(prev) }
}

// runHandler invokes a command handler with the given args and an
// empty stdin. Returns the wrapped error plus stdout/stderr buffers
// — same shape as the runAccept / runShow / runUpdate helpers in the
// per-command tests, but generic so contract sub-tests can iterate
// without duplicating the boilerplate per command.
func runHandler(t *testing.T, h handlerFn, s store.Store, cfg config.Config, args []string, stdin string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := h(context.Background(), cfg, s, args, strings.NewReader(stdin), &out, &errb)
	return err, out.String(), errb.String()
}

// TestShowJSONHasRequiredFields pins spec §Task Entity Schema. Every
// field promised by quest show appears as a top-level key, and the
// keys appear in declaration order — a refactor that swaps the
// showResponse struct for a `map[string]any` would silently sort them
// alphabetically.
func TestShowJSONHasRequiredFields(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	err, stdout, _ := runHandler(t, command.Show, s, baseCfg(), []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	required := []string{
		"id", "title", "description", "context", "type", "status",
		"role", "tier", "tags", "parent", "acceptance_criteria",
		"metadata", "owner_session", "started_at", "completed_at",
		"dependencies", "prs", "notes", "handoff", "handoff_session",
		"handoff_written_at", "debrief",
	}
	testutil.AssertSchema(t, []byte(stdout), required)
	testutil.AssertJSONKeyOrder(t, []byte(stdout), required)
}

// TestShowMissingTaskExitsThree pins spec §Error precedence:
// existence wins over usage. A show against an unknown ID wraps
// ErrNotFound (exit 3), not ErrUsage (exit 2).
func TestShowMissingTaskExitsThree(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runHandler(t, command.Show, s, baseCfg(), []string{"proj-nope"}, "")
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Errorf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestAcceptOutputShape pins {"id","status":"accepted"} — both fields
// always present, status the literal string.
func TestAcceptOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	cfg := baseCfg()
	cfg.Agent.Role = "worker"
	cfg.Agent.Session = "sess-w1"
	err, stdout, _ := runHandler(t, command.Accept, s, cfg, []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	testutil.AssertSchema(t, []byte(stdout), []string{"id", "status"})
	testutil.AssertJSONKeyOrder(t, []byte(stdout), []string{"id", "status"})
	var ack struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if ack.Status != "accepted" {
		t.Errorf("status = %q, want \"accepted\"", ack.Status)
	}
}

// TestCompleteOutputShape pins {"id","status":"complete"}.
func TestCompleteOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	cfg := workerCfg("sess-w1")
	err, stdout, _ := runHandler(t, command.Complete, s, cfg, []string{"proj-a1", "--debrief", "done"}, "")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	testutil.AssertJSONKeyOrder(t, []byte(stdout), []string{"id", "status"})
	var ack struct {
		ID, Status string
	}
	_ = json.Unmarshal([]byte(stdout), &ack)
	if ack.Status != "complete" {
		t.Errorf("status = %q, want \"complete\"", ack.Status)
	}
}

// TestFailOutputShape pins {"id","status":"failed"}.
func TestFailOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	cfg := workerCfg("sess-w1")
	err, stdout, _ := runHandler(t, command.Fail, s, cfg, []string{"proj-a1", "--debrief", "could not"}, "")
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	testutil.AssertJSONKeyOrder(t, []byte(stdout), []string{"id", "status"})
	var ack struct {
		ID, Status string
	}
	_ = json.Unmarshal([]byte(stdout), &ack)
	if ack.Status != "failed" {
		t.Errorf("status = %q, want \"failed\"", ack.Status)
	}
}

// TestCreateOutputShape pins {"id":"<new-id>"} as the only field —
// no echo of planning args.
func TestCreateOutputShape(t *testing.T) {
	s, _ := testStore(t)
	cfg := plannerCfg()
	cfg.Workspace.IDPrefix = "proj"

	err, stdout, _ := runHandler(t, command.Create, s, cfg, []string{"--title", "New"}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var raw map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(stdout), &raw); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if len(raw) != 1 {
		t.Errorf("create ack should have exactly 1 key; got %d (%v)", len(raw), keysOf(raw))
	}
	if _, ok := raw["id"]; !ok {
		t.Errorf("create ack missing id; got %v", keysOf(raw))
	}
}

// TestUpdateOutputShape pins {"id":"<id>"} for both worker-only and
// elevated-flag invocations.
func TestUpdateOutputShape(t *testing.T) {
	t.Run("worker-note", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")
		err, stdout, _ := runHandler(t, command.Update, s, workerCfg("sess-w1"),
			[]string{"proj-a1", "--note", "n"}, "")
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		testutil.AssertSchema(t, []byte(stdout), []string{"id"})
	})
	t.Run("planner-elevated-flag", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")
		err, stdout, _ := runHandler(t, command.Update, s, plannerCfg(),
			[]string{"proj-a1", "--tier", "T2"}, "")
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		testutil.AssertSchema(t, []byte(stdout), []string{"id"})
	})
}

// TestLinkOutputShape pins {"task","target","type"} for both happy
// path and idempotent no-op (second link emits same body).
func TestLinkOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")

	want := []string{"task", "target", "type"}
	for _, label := range []string{"first", "idempotent-second"} {
		err, stdout, _ := runHandler(t, command.Link, s, plannerCfg(),
			[]string{"proj-a1", "--blocked-by", "proj-a2"}, "")
		if err != nil {
			t.Fatalf("Link (%s): %v", label, err)
		}
		testutil.AssertSchema(t, []byte(stdout), want)
		testutil.AssertJSONKeyOrder(t, []byte(stdout), want)
	}
}

// TestUnlinkOutputShape pins the same {"task","target","type"} body
// for unlink, including the missing-edge no-op which still emits the
// edge identifier.
func TestUnlinkOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")
	// Add an edge then unlink it twice — second is the no-op.
	if err, _, _ := runHandler(t, command.Link, s, plannerCfg(),
		[]string{"proj-a1", "--blocked-by", "proj-a2"}, ""); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	want := []string{"task", "target", "type"}
	for _, label := range []string{"removal", "idempotent-noop"} {
		err, stdout, _ := runHandler(t, command.Unlink, s, plannerCfg(),
			[]string{"proj-a1", "--blocked-by", "proj-a2"}, "")
		if err != nil {
			t.Fatalf("Unlink (%s): %v", label, err)
		}
		testutil.AssertSchema(t, []byte(stdout), want)
		testutil.AssertJSONKeyOrder(t, []byte(stdout), want)
	}
}

// TestTagOutputShape pins {"id","tags":[...]} (post-state list, sorted).
func TestTagOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	err, stdout, _ := runHandler(t, command.Tag, s, plannerCfg(),
		[]string{"proj-a1", "go,auth"}, "")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	testutil.AssertSchema(t, []byte(stdout), []string{"id", "tags"})
	testutil.AssertJSONKeyOrder(t, []byte(stdout), []string{"id", "tags"})

	// Idempotent re-tag still emits the same shape.
	err, stdout, _ = runHandler(t, command.Tag, s, plannerCfg(),
		[]string{"proj-a1", "go"}, "")
	if err != nil {
		t.Fatalf("Tag noop: %v", err)
	}
	testutil.AssertSchema(t, []byte(stdout), []string{"id", "tags"})
}

// TestUntagOutputShape pins the same {"id","tags":[...]} body for
// untag, including the missing-tag no-op.
func TestUntagOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	if err, _, _ := runHandler(t, command.Tag, s, plannerCfg(),
		[]string{"proj-a1", "go,auth"}, ""); err != nil {
		t.Fatalf("seed tag: %v", err)
	}

	for _, label := range []string{"removal", "noop"} {
		err, stdout, _ := runHandler(t, command.Untag, s, plannerCfg(),
			[]string{"proj-a1", "absent"}, "")
		if err != nil {
			t.Fatalf("Untag (%s): %v", label, err)
		}
		testutil.AssertSchema(t, []byte(stdout), []string{"id", "tags"})
	}
}

// TestDepsOutputShape pins the per-row dependency object shape:
// {id, type, title, status}. Empty list emits [].
func TestDepsOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "Upstream", "", "open")
	if err, _, _ := runHandler(t, command.Link, s, plannerCfg(),
		[]string{"proj-a1", "--blocked-by", "proj-a2"}, ""); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	err, stdout, _ := runHandler(t, command.Deps, s, plannerCfg(), []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	var rows []map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(stdout), &rows); jerr != nil {
		t.Fatalf("not JSON array: %v; raw=%q", jerr, stdout)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	for _, k := range []string{"id", "type", "title", "status"} {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("dep row missing %q: %v", k, rows[0])
		}
	}
}

// TestMoveOutputShape pins {"id","renames":[{"old","new"}, ...]} with
// renames non-empty (always at least the moved task).
func TestMoveOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-b1", "B", "", "open")

	err, stdout, _ := runHandler(t, command.Move, s, plannerCfg(),
		[]string{"proj-a1", "--parent", "proj-b1"}, "")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	testutil.AssertSchema(t, []byte(stdout), []string{"id", "renames"})
	testutil.AssertJSONKeyOrder(t, []byte(stdout), []string{"id", "renames"})
	var ack struct {
		ID      string
		Renames []struct {
			Old, New string
		}
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if len(ack.Renames) == 0 {
		t.Errorf("renames must have at least one entry on success; got 0")
	}
	for i, r := range ack.Renames {
		if r.Old == "" || r.New == "" {
			t.Errorf("rename[%d] = %+v, want non-empty old/new", i, r)
		}
	}
}

// TestCancelOutputShape pins {"cancelled":[...],"skipped":[...]}.
// Both arrays always present; -r on a leaf returns the target alone in
// cancelled and an empty skipped.
func TestCancelOutputShape(t *testing.T) {
	t.Run("happy-path", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
		err, stdout, _ := runHandler(t, command.Cancel, s, plannerCfg(),
			[]string{"proj-a1"}, "")
		if err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		testutil.AssertSchema(t, []byte(stdout), []string{"cancelled", "skipped"})
	})

	t.Run("idempotent-noop", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskWithStatus(t, s, "proj-a1", "A", "", "cancelled")
		err, stdout, _ := runHandler(t, command.Cancel, s, plannerCfg(),
			[]string{"proj-a1"}, "")
		if err != nil {
			t.Fatalf("Cancel noop: %v", err)
		}
		testutil.AssertSchema(t, []byte(stdout), []string{"cancelled", "skipped"})
		var ack struct {
			Cancelled []string `json:"cancelled"`
			Skipped   []any    `json:"skipped"`
		}
		_ = json.Unmarshal([]byte(stdout), &ack)
		if ack.Cancelled == nil || len(ack.Cancelled) != 0 {
			t.Errorf("cancelled = %v, want []", ack.Cancelled)
		}
		if ack.Skipped == nil || len(ack.Skipped) != 0 {
			t.Errorf("skipped = %v, want []", ack.Skipped)
		}
	})

	t.Run("recursive-leaf", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
		err, stdout, _ := runHandler(t, command.Cancel, s, plannerCfg(),
			[]string{"proj-a1", "-r"}, "")
		if err != nil {
			t.Fatalf("Cancel -r leaf: %v", err)
		}
		var ack struct {
			Cancelled []string `json:"cancelled"`
			Skipped   []any    `json:"skipped"`
		}
		_ = json.Unmarshal([]byte(stdout), &ack)
		if len(ack.Cancelled) != 1 || ack.Cancelled[0] != "proj-a1" {
			t.Errorf("cancelled = %v, want [proj-a1]", ack.Cancelled)
		}
		if len(ack.Skipped) != 0 {
			t.Errorf("skipped = %v, want []", ack.Skipped)
		}
	})
}

// TestResetOutputShape pins {"id","status":"open"} on success.
func TestResetOutputShape(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "A", "accepted", "sess-w1")
	err, stdout, _ := runHandler(t, command.Reset, s, plannerCfg(), []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	testutil.AssertSchema(t, []byte(stdout), []string{"id", "status"})
	var ack struct{ ID, Status string }
	_ = json.Unmarshal([]byte(stdout), &ack)
	if ack.Status != "open" {
		t.Errorf("status = %q, want \"open\"", ack.Status)
	}
}

// TestGraphOutputShape pins {"nodes","edges"}; each node has the
// spec-pinned shape; missing ID returns exit 2.
func TestGraphOutputShape(t *testing.T) {
	t.Run("happy-path", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
		err, stdout, _ := runHandler(t, command.Graph, s, plannerCfg(),
			[]string{"proj-a1"}, "")
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		testutil.AssertSchema(t, []byte(stdout), []string{"nodes", "edges"})

		var resp struct {
			Nodes []map[string]json.RawMessage `json:"nodes"`
			Edges []map[string]json.RawMessage `json:"edges"`
		}
		if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
			t.Fatalf("unmarshal: %v", jerr)
		}
		if len(resp.Nodes) != 1 {
			t.Fatalf("nodes = %d, want 1", len(resp.Nodes))
		}
		for _, k := range []string{"id", "title", "type", "status", "tier", "role", "children"} {
			if _, ok := resp.Nodes[0][k]; !ok {
				t.Errorf("node missing %q: %v", k, resp.Nodes[0])
			}
		}
	})

	t.Run("missing-id-exits-two", func(t *testing.T) {
		s, _ := testStore(t)
		err, _, _ := runHandler(t, command.Graph, s, plannerCfg(), nil, "")
		if !stderrors.Is(err, errors.ErrUsage) {
			t.Errorf("err = %v, want wraps ErrUsage", err)
		}
	})
}

// TestListJSONRowShape pins the per-row contract: only requested
// columns appear; null-when-unset for nullable scalars; arrays for
// collection columns; key order honors --columns.
func TestListJSONRowShape(t *testing.T) {
	t.Run("default-columns", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
		err, stdout, _ := runHandler(t, command.List, s, plannerCfg(), nil, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var rows []map[string]json.RawMessage
		if jerr := json.Unmarshal([]byte(stdout), &rows); jerr != nil {
			t.Fatalf("unmarshal: %v; raw=%q", jerr, stdout)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		for _, k := range []string{"id", "status", "blocked-by", "title"} {
			if _, ok := rows[0][k]; !ok {
				t.Errorf("default row missing %q: %v", k, rows[0])
			}
		}
		if string(rows[0]["blocked-by"]) != "[]" {
			t.Errorf("blocked-by = %s, want []", rows[0]["blocked-by"])
		}
	})

	t.Run("columns-order-preserved", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
		err, stdout, _ := runHandler(t, command.List, s, plannerCfg(),
			[]string{"--columns", "title,id"}, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		// stdout is a JSON array; isolate row 0 for ordering assertion.
		raw := strings.TrimSpace(stdout)
		raw = strings.TrimPrefix(raw, "[")
		raw = strings.TrimSuffix(raw, "]")
		// One row → trim trailing newlines/whitespace inside array.
		raw = strings.TrimSpace(raw)
		testutil.AssertJSONKeyOrder(t, []byte(raw), []string{"title", "id"})
	})

	t.Run("empty-result-is-array", func(t *testing.T) {
		s, _ := testStore(t)
		err, stdout, _ := runHandler(t, command.List, s, plannerCfg(), nil, "")
		if err != nil {
			t.Fatalf("List empty: %v", err)
		}
		got := strings.TrimSpace(stdout)
		if got != "[]" {
			t.Errorf("empty list = %q, want []", got)
		}
	})

	t.Run("unknown-column-exits-two", func(t *testing.T) {
		s, _ := testStore(t)
		err, _, _ := runHandler(t, command.List, s, plannerCfg(),
			[]string{"--columns", "id,nope"}, "")
		if !stderrors.Is(err, errors.ErrUsage) {
			t.Errorf("err = %v, want wraps ErrUsage", err)
		}
	})

	t.Run("nullable-emits-null", func(t *testing.T) {
		s, _ := testStore(t)
		seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
		err, stdout, _ := runHandler(t, command.List, s, plannerCfg(),
			[]string{"--columns", "id,role,tier,parent"}, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var rows []map[string]json.RawMessage
		if jerr := json.Unmarshal([]byte(stdout), &rows); jerr != nil {
			t.Fatalf("unmarshal: %v", jerr)
		}
		for _, k := range []string{"role", "tier", "parent"} {
			if string(rows[0][k]) != "null" {
				t.Errorf("%s = %s, want null", k, rows[0][k])
			}
		}
	})
}

// TestVersionOutputShape pins the {"version":"..."} JSON shape and the
// bare-version-string text path. Exit 0 in both cases. Note: the OTEL
// span suppression promised by the SuppressTelemetry descriptor is
// covered by the cli/contract_test.go suite; here we only assert the
// stdout shape.
func TestVersionOutputShape(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		var out bytes.Buffer
		if err := command.Version(context.Background(), baseCfg(), nil, nil, strings.NewReader(""), &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("Version: %v", err)
		}
		testutil.AssertSchema(t, out.Bytes(), []string{"version"})
	})
	t.Run("text", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Output.Format = "text"
		var out bytes.Buffer
		if err := command.Version(context.Background(), cfg, nil, nil, strings.NewReader(""), &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("Version text: %v", err)
		}
		line := strings.TrimRight(out.String(), "\n")
		if line == "" || strings.Contains(line, "{") {
			t.Errorf("text version looks wrong: %q", out.String())
		}
	})
}

// TestInitOutputShape pins {"workspace","id_prefix"} JSON and the bare
// absolute path text output. The workspace value's basename is ".quest"
// (asserts the abs-path includes the directory); --format text emits
// just the absolute path with a trailing newline.
func TestInitOutputShape(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		dir := t.TempDir()
		cwd := mustChdir(t, dir)
		t.Cleanup(cwd)

		var out, errb bytes.Buffer
		err := command.Init(context.Background(), baseCfg(), nil,
			[]string{"--prefix", "proj"}, strings.NewReader(""), &out, &errb)
		if err != nil {
			t.Fatalf("Init: %v; stderr=%q", err, errb.String())
		}
		testutil.AssertSchema(t, out.Bytes(), []string{"workspace", "id_prefix"})
		var resp struct {
			Workspace string `json:"workspace"`
			IDPrefix  string `json:"id_prefix"`
		}
		if jerr := json.Unmarshal(out.Bytes(), &resp); jerr != nil {
			t.Fatalf("unmarshal: %v", jerr)
		}
		if resp.IDPrefix != "proj" {
			t.Errorf("id_prefix = %q, want proj", resp.IDPrefix)
		}
		if !strings.HasSuffix(resp.Workspace, ".quest") {
			t.Errorf("workspace basename should be .quest; got %q", resp.Workspace)
		}
	})
	t.Run("text", func(t *testing.T) {
		dir := t.TempDir()
		cwd := mustChdir(t, dir)
		t.Cleanup(cwd)

		cfg := baseCfg()
		cfg.Output.Format = "text"
		var out, errb bytes.Buffer
		if err := command.Init(context.Background(), cfg, nil,
			[]string{"--prefix", "proj"}, strings.NewReader(""), &out, &errb); err != nil {
			t.Fatalf("Init: %v; stderr=%q", err, errb.String())
		}
		s := out.String()
		if !strings.HasSuffix(s, "\n") {
			t.Errorf("text output should end with newline; got %q", s)
		}
		if strings.Contains(s, "{") {
			t.Errorf("text output should be bare path; got %q", s)
		}
	})
}

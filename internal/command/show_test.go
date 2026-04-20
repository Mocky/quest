//go:build integration

package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// testStore opens a fresh sqlite DB at t.TempDir(), migrates it, and
// returns the live Store plus the on-disk path (for tests that need to
// inspect the DB via a sibling *sql.DB). Centralized here so every
// test in the package uses the same setup.
func testStore(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return s, path
}

// baseCfg is a workspace-backed Config for handler-level tests —
// handlers only read cfg.Agent and cfg.Output, so the workspace fields
// stay empty.
func baseCfg() config.Config {
	return config.Config{
		Agent:  config.AgentConfig{},
		Output: config.OutputConfig{Format: "json"},
	}
}

// runShow invokes command.Show with the supplied args/cfg and a real
// store, returning the exit-path error plus stdout/stderr buffers.
func runShow(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Show(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// seedMinimalTask inserts a task row with only the required columns so
// Show can read it back. Fields omitted here land as SQL NULL and
// should round-trip to JSON null in the output.
func seedMinimalTask(t *testing.T, s store.Store, id, title string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, created_at) VALUES (?, ?, ?)`,
		id, title, "2026-04-18T00:00:00Z")
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestShowMissingTaskReturnsNotFound pins the spec §Error precedence
// "existence → exit 3" rule: a show against an unknown ID wraps
// ErrNotFound.
func TestShowMissingTaskReturnsNotFound(t *testing.T) {
	s, _ := testStore(t)
	err, out, _ := runShow(t, s, baseCfg(), []string{"proj-nope"})
	if err == nil {
		t.Fatalf("Show(proj-nope): got nil error, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
	if out != "" {
		t.Errorf("stdout not empty on not-found: %q", out)
	}
}

// TestShowMissingIDReturnsUsage pins the ErrUsage result when no
// positional task ID is provided.
func TestShowMissingIDReturnsUsage(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runShow(t, s, baseCfg(), nil)
	if err == nil {
		t.Fatalf("Show(): got nil error, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestShowIgnoresAgentTaskWhenIDMissing pins the regression: worker
// commands do not fall back to AGENT_TASK when the positional ID is
// omitted. A future refactor that re-introduces the fallback fails
// this test.
func TestShowIgnoresAgentTaskWhenIDMissing(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	cfg := baseCfg()
	cfg.Agent.Task = "proj-a1"
	err, _, _ := runShow(t, s, cfg, nil)
	if err == nil {
		t.Fatalf("Show(): got nil error, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestShowUnexpectedPositionalArgs rejects trailing args with ErrUsage.
func TestShowUnexpectedPositionalArgs(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	err, _, _ := runShow(t, s, baseCfg(), []string{"proj-a1", "proj-a2"})
	if err == nil {
		t.Fatalf("got nil error, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestShowEmitsAllFieldsWithNulls walks the spec's "all fields always
// present" invariant. An untouched task row emits every documented
// field; nullable columns unwritten at insert time land as JSON null,
// collections land as [], and metadata lands as {}.
func TestShowEmitsAllFieldsWithNulls(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	err, stdout, _ := runShow(t, s, baseCfg(), []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}

	var raw map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(stdout), &raw); jerr != nil {
		t.Fatalf("stdout not JSON object: %v; raw=%q", jerr, stdout)
	}
	required := []string{
		"id", "title", "description", "context", "type", "status",
		"role", "tier", "tags", "parent", "acceptance_criteria",
		"metadata", "owner_session", "started_at", "completed_at",
		"dependencies", "prs", "notes", "handoff", "handoff_session",
		"handoff_written_at", "debrief",
	}
	for _, k := range required {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing required key %q", k)
		}
	}

	// --history absent by default (the one spec carve-out).
	if _, ok := raw["history"]; ok {
		t.Errorf("history field present without --history flag")
	}

	// Nullable scalars emit JSON null, not "".
	nullFields := []string{"role", "tier", "parent", "acceptance_criteria",
		"owner_session", "started_at", "completed_at",
		"handoff", "handoff_session", "handoff_written_at", "debrief"}
	for _, k := range nullFields {
		if string(raw[k]) != "null" {
			t.Errorf("%s = %s, want null", k, raw[k])
		}
	}
	// Collections emit [] / {} for empty state.
	if string(raw["tags"]) != "[]" {
		t.Errorf("tags = %s, want []", raw["tags"])
	}
	if string(raw["dependencies"]) != "[]" {
		t.Errorf("dependencies = %s, want []", raw["dependencies"])
	}
	if string(raw["prs"]) != "[]" {
		t.Errorf("prs = %s, want []", raw["prs"])
	}
	if string(raw["notes"]) != "[]" {
		t.Errorf("notes = %s, want []", raw["notes"])
	}
	if string(raw["metadata"]) != "{}" {
		t.Errorf("metadata = %s, want {}", raw["metadata"])
	}
}

// TestShowWithDepsDenormalizesTargetTitleAndStatus seeds a dependency
// edge and asserts `quest show` returns the target's title and status
// inline (spec §quest show).
func TestShowWithDepsDenormalizesTargetTitleAndStatus(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Upstream")
	seedMinimalTask(t, s, "proj-a2", "Downstream")

	tx, err := s.BeginImmediate(context.Background(), store.TxLink)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO dependencies(task_id, target_id, link_type, created_at)
		 VALUES ('proj-a2', 'proj-a1', 'blocked-by', '2026-04-18T01:00:00Z')`)
	if err != nil {
		t.Fatalf("insert dep: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err, stdout, _ := runShow(t, s, baseCfg(), []string{"proj-a2"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	var resp struct {
		Dependencies []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Status   string `json:"status"`
			Type     string `json:"type"`
			LinkType string `json:"link_type"`
		} `json:"dependencies"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if len(resp.Dependencies) != 1 {
		t.Fatalf("dependencies = %d, want 1", len(resp.Dependencies))
	}
	d := resp.Dependencies[0]
	if d.ID != "proj-a1" || d.LinkType != "blocked-by" || d.Title != "Upstream" || d.Status != "open" || d.Type != "task" {
		t.Errorf("dep = %+v, want {id=proj-a1, link_type=blocked-by, title=Upstream, status=open, type=task}", d)
	}
}

// TestShowHistoryFieldPresence covers the three states the history
// carve-out promises: default (absent), --history on empty history
// (present as []), --history populated (present with rows).
func TestShowHistoryFieldPresence(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	// Default: history absent.
	err, stdout, _ := runShow(t, s, baseCfg(), []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show default: %v", err)
	}
	var raw map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(stdout), &raw); jerr != nil {
		t.Fatalf("default JSON: %v", jerr)
	}
	if _, ok := raw["history"]; ok {
		t.Errorf("default: history key present; want absent")
	}

	// --history with no history: empty array.
	err, stdout, _ = runShow(t, s, baseCfg(), []string{"--history", "proj-a1"})
	if err != nil {
		t.Fatalf("Show --history empty: %v", err)
	}
	raw = nil
	if jerr := json.Unmarshal([]byte(stdout), &raw); jerr != nil {
		t.Fatalf("--history empty JSON: %v", jerr)
	}
	h, ok := raw["history"]
	if !ok {
		t.Fatalf("--history empty: history key missing")
	}
	if string(h) != "[]" {
		t.Errorf("--history empty: history = %s, want []", h)
	}

	// Insert a history row, then --history populates.
	tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if err := store.AppendHistory(context.Background(), tx, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T02:00:00Z",
		Action:    store.HistoryCreated,
		Role:      "planner",
		Session:   "sess-1",
	}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err, stdout, _ = runShow(t, s, baseCfg(), []string{"--history", "proj-a1"})
	if err != nil {
		t.Fatalf("Show --history populated: %v", err)
	}
	var resp2 struct {
		History []struct {
			Timestamp string  `json:"timestamp"`
			Role      *string `json:"role"`
			Session   *string `json:"session"`
			Action    string  `json:"action"`
		} `json:"history"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp2); jerr != nil {
		t.Fatalf("--history populated JSON: %v", jerr)
	}
	if len(resp2.History) != 1 {
		t.Fatalf("history = %d entries, want 1", len(resp2.History))
	}
	entry := resp2.History[0]
	if entry.Action != "created" || entry.Timestamp != "2026-04-18T02:00:00Z" {
		t.Errorf("entry = %+v, want action=created timestamp=2026-04-18T02:00:00Z", entry)
	}
	if entry.Role == nil || *entry.Role != "planner" {
		t.Errorf("entry.role = %v, want planner", entry.Role)
	}
	if entry.Session == nil || *entry.Session != "sess-1" {
		t.Errorf("entry.session = %v, want sess-1", entry.Session)
	}
}

// TestShowHistoryFlattensPayloadKeys pins the cross-cutting rule: the
// stored payload JSON is lifted into the top-level of the entry object
// (spec §History field). A reset row with a reason should emit
// {"timestamp":..., "role":..., "session":..., "action":"reset",
// "reason":"..."}.
func TestShowHistoryFlattensPayloadKeys(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	tx, err := s.BeginImmediate(context.Background(), store.TxReset)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if err := store.AppendHistory(context.Background(), tx, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T03:00:00Z",
		Action:    store.HistoryReset,
		Role:      "planner",
		Session:   "sess-2",
		Payload:   map[string]any{"reason": "session crashed"},
	}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err, stdout, _ := runShow(t, s, baseCfg(), []string{"--history", "proj-a1"})
	if err != nil {
		t.Fatalf("Show --history: %v", err)
	}
	var resp struct {
		History []map[string]json.RawMessage `json:"history"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("JSON: %v", jerr)
	}
	if len(resp.History) != 1 {
		t.Fatalf("history = %d entries, want 1", len(resp.History))
	}
	entry := resp.History[0]
	if r, ok := entry["reason"]; !ok {
		t.Errorf("payload key reason missing from flattened entry: %v", entry)
	} else if string(r) != `"session crashed"` {
		t.Errorf("reason = %s, want \"session crashed\"", r)
	}
}

// TestShowParentIsObjectWhenSet pins the spec §quest show denormalized
// parent: when the task has a parent, `parent` renders as the
// four-field taskref cluster `{id, title, status, type}`; otherwise
// null. The shape is load-bearing for agents that read the parent's
// status without a second `quest show` call.
func TestShowParentIsObjectWhenSet(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Auth module")
	// Insert child directly to bypass quest create parent-status
	// preconditions — the renderer only cares about row shape, not
	// create-time validation.
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, type, status, parent, created_at)
		 VALUES ('proj-a1.1', 'JWT validation', 'task', 'open', 'proj-a1', ?)`,
		"2026-04-18T01:00:00Z"); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Non-root: parent is an object with all four keys populated.
	err, stdout, _ := runShow(t, s, baseCfg(), []string{"proj-a1.1"})
	if err != nil {
		t.Fatalf("Show child: %v", err)
	}
	var resp struct {
		Parent *struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
			Type   string `json:"type"`
		} `json:"parent"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("stdout JSON: %v; raw=%q", jerr, stdout)
	}
	if resp.Parent == nil {
		t.Fatalf("parent = null, want object; stdout=%q", stdout)
	}
	if resp.Parent.ID != "proj-a1" || resp.Parent.Title != "Auth module" ||
		resp.Parent.Status != "open" || resp.Parent.Type != "task" {
		t.Errorf("parent = %+v, want {id=proj-a1, title=Auth module, status=open, type=task}",
			resp.Parent)
	}

	// Root: parent is JSON null.
	err, stdout, _ = runShow(t, s, baseCfg(), []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show root: %v", err)
	}
	var raw map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(stdout), &raw); jerr != nil {
		t.Fatalf("root JSON: %v", jerr)
	}
	if string(raw["parent"]) != "null" {
		t.Errorf("root parent = %s, want null", raw["parent"])
	}
}

// TestShowTextHeaderHasBugMarker pins the spec §Header rule: when the
// task's type is bug, the header gets a ` (bug) ` marker between the
// status bracket and the title. Default `task` omits the marker.
func TestShowTextHeaderHasBugMarker(t *testing.T) {
	s, _ := testStore(t)
	// Seed one task of each classification.
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, type, status, created_at) VALUES
		 ('proj-a1', 'Plain task', 'task', 'open', ?),
		 ('proj-a2', 'Nasty regression', 'bug', 'open', ?)`,
		"2026-04-18T00:00:00Z", "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Format = "text"

	err, out1, _ := runShow(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show proj-a1: %v", err)
	}
	first := firstLine(out1)
	if first != "proj-a1 [open] Plain task" {
		t.Errorf("task header = %q, want \"proj-a1 [open] Plain task\"", first)
	}

	err, out2, _ := runShow(t, s, cfg, []string{"proj-a2"})
	if err != nil {
		t.Fatalf("Show proj-a2: %v", err)
	}
	first = firstLine(out2)
	if first != "proj-a2 [open] (bug) Nasty regression" {
		t.Errorf("bug header = %q, want \"proj-a2 [open] (bug) Nasty regression\"", first)
	}
}

// TestShowTextMinimalTask pins the smallest possible rendering: a
// bare task has a header, an `exec` row is absent when tier is unset,
// and no sections follow.
func TestShowTextMinimalTask(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	cfg := baseCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runShow(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := "proj-a1 [open] Alpha\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestShowTextMetadataCluster pins the 4-space indent, the widest-key
// padding, and the presence rules for parent / tags / exec /
// metadata / started / completed.
func TestShowTextMetadataCluster(t *testing.T) {
	s, _ := testStore(t)
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, type, status, tier, role, owner_session, metadata, created_at) VALUES
		 ('proj-a1', 'Parent', 'bug', 'open', 'T1', 'planner', NULL, '{}', ?),
		 ('proj-a1.1', 'Child', 'task', 'open', 'T3', 'coder', 'sess-1', '{"priority":"high"}', ?)`,
		"2026-04-18T00:00:00Z", "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE tasks SET parent='proj-a1' WHERE id='proj-a1.1'`); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tags(task_id, tag) VALUES ('proj-a1.1', 'auth'), ('proj-a1.1', 'go')`); err != nil {
		t.Fatalf("insert tags: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runShow(t, s, cfg, []string{"proj-a1.1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	// Widest key is "metadata" (8) → value column starts at 4+8+2 = 14.
	// Parent row: parent cluster with (bug) marker since proj-a1.type=bug.
	// Tags row: comma-space join. Exec: T3 - coder - sess-1. Metadata:
	// priority=high.
	wantLines := []string{
		"proj-a1.1 [open] Child",
		"    parent    proj-a1 [open] (bug) Parent",
		"    tags      auth, go",
		"    exec      T3 - coder - sess-1",
		"    metadata  priority=high",
	}
	got := strings.Split(stdout, "\n")
	for i, want := range wantLines {
		if i >= len(got) {
			t.Fatalf("line %d missing; stdout=%q", i, stdout)
		}
		if got[i] != want {
			t.Errorf("line %d = %q, want %q", i, got[i], want)
		}
	}
}

// TestShowTextExecTrailingNullDrops pins the spec's trailing-null
// behavior: a task with tier+role but no owner_session renders
// `T3 - coder` (no trailing em-dash or separator).
func TestShowTextExecTrailingNullDrops(t *testing.T) {
	s, _ := testStore(t)
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, type, status, tier, role, created_at) VALUES
		 ('proj-a1', 'Alpha', 'task', 'open', 'T3', 'coder', ?)`,
		"2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runShow(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(stdout, "exec  T3 - coder\n") {
		t.Errorf("missing exec row with trailing-null drop: %q", stdout)
	}
	// Must NOT contain a dangling separator or em-dash.
	if strings.Contains(stdout, "T3 - coder - ") || strings.Contains(stdout, "T3 - coder -—") {
		t.Errorf("unexpected trailing separator: %q", stdout)
	}
}

// TestShowTextDependenciesSection pins the Dependencies body: rows
// are 4-indented, the link_type column pads to the widest value, and
// each target renders with the `(bug)` marker when appropriate.
func TestShowTextDependenciesSection(t *testing.T) {
	s, _ := testStore(t)
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, type, status, created_at) VALUES
		 ('proj-a1', 'JWT',    'task', 'completed', ?),
		 ('proj-a2', 'Crash',  'bug',  'completed', ?),
		 ('proj-a3', 'Middle', 'task', 'open',      ?)`,
		"2026-04-18T00:00:00Z", "2026-04-18T00:00:00Z", "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert tasks: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES
		 ('proj-a3', 'proj-a1', 'blocked-by',  ?),
		 ('proj-a3', 'proj-a2', 'caused-by',   ?)`,
		"2026-04-18T01:00:00Z", "2026-04-18T01:00:01Z"); err != nil {
		t.Fatalf("insert deps: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runShow(t, s, cfg, []string{"proj-a3"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	// Widest link_type is "blocked-by" (10). Both rows pad to 10+2
	// spaces before the target's task-ref cluster.
	wantBlocked := "    blocked-by  proj-a1 [completed] JWT"
	wantCaused := "    caused-by   proj-a2 [completed] (bug) Crash"
	if !strings.Contains(stdout, wantBlocked) {
		t.Errorf("missing blocked-by row %q in %q", wantBlocked, stdout)
	}
	if !strings.Contains(stdout, wantCaused) {
		t.Errorf("missing caused-by row %q in %q", wantCaused, stdout)
	}
	if !strings.Contains(stdout, "\nDependencies\n") {
		t.Errorf("missing Dependencies heading: %q", stdout)
	}
}

// TestShowTextDebriefMissingOnCompleted pins the spec's
// (missing) debrief rule: a completed task with null debrief renders
// the Debrief section with the literal `(missing)` body.
func TestShowTextDebriefMissingOnCompleted(t *testing.T) {
	s, _ := testStore(t)
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, type, status, created_at) VALUES
		 ('proj-a1', 'Done', 'task', 'completed', ?)`,
		"2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runShow(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(stdout, "\nDebrief\n    (missing)\n") {
		t.Errorf("missing debrief (missing) body: %q", stdout)
	}
}

// TestShowTextPipedWraps80 pins the spec's piped-output width: when
// the writer is not a TTY (bytes.Buffer in this test), prose sections
// wrap at 80 columns (innerWidth 76 after the 4-space indent). Uses
// space-separated short words so the word-wrapper has break points —
// a single token longer than width overflows by design, matching the
// spec's "long lines overflow; truncation is not used for show".
func TestShowTextPipedWraps80(t *testing.T) {
	s, _ := testStore(t)
	words := strings.Repeat("alpha beta gamma delta epsilon ", 20) // 600+ chars of 5-7 letter words
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, type, status, description, created_at) VALUES
		 ('proj-a1', 'Alpha', 'task', 'open', ?, ?)`,
		words, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runShow(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	// Find the Description body lines and assert the 4-space indent
	// prefix plus that no line exceeds 80 columns.
	var bodyLines []string
	collect := false
	for _, line := range strings.Split(stdout, "\n") {
		if line == "Description" {
			collect = true
			continue
		}
		if collect {
			if line == "" {
				break
			}
			bodyLines = append(bodyLines, line)
		}
	}
	if len(bodyLines) < 2 {
		t.Fatalf("description did not wrap: %d lines, raw=%q", len(bodyLines), stdout)
	}
	for i, l := range bodyLines {
		if !strings.HasPrefix(l, "    ") {
			t.Errorf("line %d missing 4-space indent: %q", i, l)
		}
		if len(l) > 80 {
			t.Errorf("line %d exceeds 80 cols (%d): %q", i, len(l), l)
		}
	}
}

// TestShowTextHistoryBlock pins the --history section: heading carries
// the count, each row shows `{ts}  {role}/{session}  {action}` with
// action-specific detail when applicable.
func TestShowTextHistoryBlock(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if err := store.AppendHistory(context.Background(), tx, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T10:00:00Z",
		Action:    store.HistoryCreated,
		Role:      "planner",
		Session:   "sess-p1",
		Payload:   map[string]any{"tier": "T2", "role": "coder", "tags": []any{"go", "auth"}},
	}); err != nil {
		t.Fatalf("append created: %v", err)
	}
	if err := store.AppendHistory(context.Background(), tx, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T10:05:00Z",
		Action:    store.HistoryAccepted,
		Role:      "coder",
		Session:   "sess-c1",
	}); err != nil {
		t.Fatalf("append accepted: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runShow(t, s, cfg, []string{"--history", "proj-a1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(stdout, "\nHistory (2)\n") {
		t.Errorf("missing History heading with count: %q", stdout)
	}
	// `created` detail: key=value with list rendering for tags.
	if !strings.Contains(stdout, "role=coder tags=[go,auth] tier=T2") {
		t.Errorf("missing created detail: %q", stdout)
	}
	// `accepted` row has no detail column.
	if !strings.Contains(stdout, "planner/sess-p1") {
		t.Errorf("missing role/session for created row: %q", stdout)
	}
	if !strings.Contains(stdout, "coder/sess-c1") {
		t.Errorf("missing role/session for accepted row: %q", stdout)
	}
}

// firstLine returns the first line of s (no trailing newline). Used
// by header-only assertions where the remainder of stdout is noise.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestShowNullHistoryRoleSession pins NULL round-trip: storing an
// empty role/session should surface as JSON null (via *string), never
// "". Proves cross-cutting.md §Nullable TEXT columns.
func TestShowNullHistoryRoleSession(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if err := store.AppendHistory(context.Background(), tx, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T04:00:00Z",
		Action:    store.HistoryNoteAdded,
	}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err, stdout, _ := runShow(t, s, baseCfg(), []string{"--history", "proj-a1"})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	var resp struct {
		History []map[string]json.RawMessage `json:"history"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("JSON: %v", jerr)
	}
	if len(resp.History) != 1 {
		t.Fatalf("history = %d, want 1", len(resp.History))
	}
	if string(resp.History[0]["role"]) != "null" {
		t.Errorf("role = %s, want null", resp.History[0]["role"])
	}
	if string(resp.History[0]["session"]) != "null" {
		t.Errorf("session = %s, want null", resp.History[0]["session"])
	}
}

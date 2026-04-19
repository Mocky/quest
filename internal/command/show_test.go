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

// TestShowDefaultsToAgentTask verifies AGENT_TASK fills in when the
// positional ID is omitted. Config flows through cfg.Agent.Task.
func TestShowDefaultsToAgentTask(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	cfg := baseCfg()
	cfg.Agent.Task = "proj-a1"
	err, stdout, _ := runShow(t, s, cfg, nil)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	var got struct {
		ID string `json:"id"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if got.ID != "proj-a1" {
		t.Errorf("id = %q, want proj-a1", got.ID)
	}
}

// TestShowMissingIDAndNoAgentTask pins the ErrUsage fallback when
// neither the positional ID nor AGENT_TASK is set.
func TestShowMissingIDAndNoAgentTask(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runShow(t, s, baseCfg(), nil)
	if err == nil {
		t.Fatalf("Show(): got nil error, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "AGENT_TASK is unset") {
		t.Errorf("err = %q, want mentions AGENT_TASK", err.Error())
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
			ID     string `json:"id"`
			Type   string `json:"type"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"dependencies"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if len(resp.Dependencies) != 1 {
		t.Fatalf("dependencies = %d, want 1", len(resp.Dependencies))
	}
	d := resp.Dependencies[0]
	if d.ID != "proj-a1" || d.Type != "blocked-by" || d.Title != "Upstream" || d.Status != "open" {
		t.Errorf("dep = %+v, want {id=proj-a1, type=blocked-by, title=Upstream, status=open}", d)
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

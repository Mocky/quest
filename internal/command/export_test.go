//go:build integration

package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/store"
)

func runExport(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Export(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// exportCfg matches plannerCfg but lets each test pin the workspace
// root so the default --dir resolution lands inside t.TempDir().
func exportCfg(workspaceRoot string) config.Config {
	return config.Config{
		Workspace: config.WorkspaceConfig{Root: workspaceRoot, ElevatedRoles: []string{"planner"}},
		Agent:     config.AgentConfig{Role: "planner", Session: "sess-p1"},
		Output:    config.OutputConfig{Format: "json"},
	}
}

// TestExportCreatesLayout pins spec §`quest export`'s directory shape:
// `tasks/`, `debriefs/`, and `history.jsonl` always present; per-task
// JSON for every task; debrief markdown only for tasks with non-empty
// debrief; debriefs/ directory always created even when no task has
// a debrief (phase-11-export.md implementation note).
func TestExportCreatesLayout(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	seedMinimalTask(t, s, "proj-a2", "Beta")

	// Give proj-a1 a debrief so debriefs/ has one file; proj-a2 stays
	// debrief-less so the rest-of-dir contract exercises the no-debrief
	// case without hiding the "always create debriefs/" rule.
	setDebrief(t, s, "proj-a1", "# Alpha\n\nAll good.\n")

	workspace := t.TempDir()
	err, stdout, _ := runExport(t, s, exportCfg(workspace), nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	var ack struct {
		Dir            string `json:"dir"`
		Tasks          int    `json:"tasks"`
		Debriefs       int    `json:"debriefs"`
		HistoryEntries int    `json:"history_entries"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	wantDir := filepath.Join(workspace, "quest-export")
	if ack.Dir != wantDir {
		t.Errorf("ack.Dir = %q, want %q", ack.Dir, wantDir)
	}
	if ack.Tasks != 2 {
		t.Errorf("ack.Tasks = %d, want 2", ack.Tasks)
	}
	if ack.Debriefs != 1 {
		t.Errorf("ack.Debriefs = %d, want 1", ack.Debriefs)
	}

	requireFile(t, filepath.Join(wantDir, "tasks", "proj-a1.json"))
	requireFile(t, filepath.Join(wantDir, "tasks", "proj-a2.json"))
	requireFile(t, filepath.Join(wantDir, "debriefs", "proj-a1.md"))
	requireNoFile(t, filepath.Join(wantDir, "debriefs", "proj-a2.md"))
	requireFile(t, filepath.Join(wantDir, "history.jsonl"))
}

// TestExportEmptyDatabaseStillCreatesDebriefsDir pins the
// implementation note "Always create the `debriefs/` directory even
// when no task has a debrief" — downstream consumers that
// pattern-match the on-disk shape should not have to handle a missing
// directory.
func TestExportEmptyDatabaseStillCreatesDebriefsDir(t *testing.T) {
	s, _ := testStore(t)
	workspace := t.TempDir()
	err, _, _ := runExport(t, s, exportCfg(workspace), nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	info, statErr := os.Stat(filepath.Join(workspace, "quest-export", "debriefs"))
	if statErr != nil {
		t.Fatalf("debriefs/ not created on empty export: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("debriefs/ is not a directory")
	}
}

// TestExportTaskMatchesShowHistory is the Layer 2 contract: a task's
// exported JSON is field-for-field equivalent to `quest show --history`
// output. Phase 11 spec anchor: "Each task JSON file contains the
// complete task entity (same schema as `quest show --history`
// output)."
func TestExportTaskMatchesShowHistory(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	setDebrief(t, s, "proj-a1", "Debrief body")

	// Add a history row with a payload so the flattening rule is
	// exercised (reason lifts to top-level alongside action).
	appendHistoryRow(t, s, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T01:00:00Z",
		Action:    store.HistoryReset,
		Role:      "planner",
		Session:   "sess-p1",
		Payload:   map[string]any{"reason": "session crashed"},
	})

	workspace := t.TempDir()
	if err, _, _ := runExport(t, s, exportCfg(workspace), nil); err != nil {
		t.Fatalf("Export: %v", err)
	}

	err, showOut, _ := runShow(t, s, exportCfg(workspace), []string{"--history", "proj-a1"})
	if err != nil {
		t.Fatalf("Show --history: %v", err)
	}

	exportBytes, rerr := os.ReadFile(filepath.Join(workspace, "quest-export", "tasks", "proj-a1.json"))
	if rerr != nil {
		t.Fatalf("read export task file: %v", rerr)
	}

	var showObj, exportObj map[string]any
	if jerr := json.Unmarshal([]byte(showOut), &showObj); jerr != nil {
		t.Fatalf("show JSON: %v", jerr)
	}
	if jerr := json.Unmarshal(exportBytes, &exportObj); jerr != nil {
		t.Fatalf("export JSON: %v", jerr)
	}

	showKeys := sortedKeys(showObj)
	exportKeys := sortedKeys(exportObj)
	if !equalStrings(showKeys, exportKeys) {
		t.Fatalf("export field set %v != show --history field set %v", exportKeys, showKeys)
	}
	for k := range showObj {
		if !jsonEqual(showObj[k], exportObj[k]) {
			sb, _ := json.Marshal(showObj[k])
			eb, _ := json.Marshal(exportObj[k])
			t.Errorf("field %q differs: show=%s export=%s", k, sb, eb)
		}
	}
}

// TestExportHistoryJSONLChronological pins spec §`quest export`:
// history.jsonl is "chronological across all tasks" and phase note:
// payload keys flatten into the top level, plus task_id is a
// canonical field (added for the cross-task stream).
func TestExportHistoryJSONLChronological(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	seedMinimalTask(t, s, "proj-a2", "Beta")

	appendHistoryRow(t, s, store.History{
		TaskID:    "proj-a2",
		Timestamp: "2026-04-18T02:00:00Z",
		Action:    store.HistoryCreated,
		Role:      "planner",
		Session:   "sess-1",
	})
	appendHistoryRow(t, s, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T01:00:00Z",
		Action:    store.HistoryCreated,
		Role:      "planner",
		Session:   "sess-1",
	})
	appendHistoryRow(t, s, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T03:00:00Z",
		Action:    store.HistoryReset,
		Role:      "planner",
		Session:   "sess-1",
		Payload:   map[string]any{"reason": "rebuild"},
	})

	workspace := t.TempDir()
	if err, _, _ := runExport(t, s, exportCfg(workspace), nil); err != nil {
		t.Fatalf("Export: %v", err)
	}

	raw, rerr := os.ReadFile(filepath.Join(workspace, "quest-export", "history.jsonl"))
	if rerr != nil {
		t.Fatalf("read history.jsonl: %v", rerr)
	}
	lines := splitJSONL(string(raw))
	if len(lines) != 3 {
		t.Fatalf("jsonl lines = %d, want 3; raw=%q", len(lines), raw)
	}
	var entries []map[string]any
	for i, line := range lines {
		var m map[string]any
		if jerr := json.Unmarshal([]byte(line), &m); jerr != nil {
			t.Fatalf("line %d not JSON: %v; raw=%q", i, jerr, line)
		}
		entries = append(entries, m)
	}
	if entries[0]["timestamp"] != "2026-04-18T01:00:00Z" ||
		entries[1]["timestamp"] != "2026-04-18T02:00:00Z" ||
		entries[2]["timestamp"] != "2026-04-18T03:00:00Z" {
		t.Errorf("jsonl not sorted by timestamp; got %v %v %v",
			entries[0]["timestamp"], entries[1]["timestamp"], entries[2]["timestamp"])
	}
	if entries[0]["task_id"] != "proj-a1" || entries[1]["task_id"] != "proj-a2" {
		t.Errorf("task_id missing or wrong on early entries: %v %v",
			entries[0]["task_id"], entries[1]["task_id"])
	}
	if entries[2]["reason"] != "rebuild" {
		t.Errorf("payload `reason` not flattened on entry 2: %v", entries[2])
	}
	// action should remain "reset" after flattening (reserved key
	// protection: payload never shadows canonical attributes).
	if entries[2]["action"] != "reset" {
		t.Errorf("action on entry 2 = %v, want reset", entries[2]["action"])
	}
}

// TestExportIdempotent runs export twice and verifies the tree is
// byte-identical. Phase 11 Done-when: "round-trips the full database
// and produces files that are human-readable and diff-friendly."
func TestExportIdempotent(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	setDebrief(t, s, "proj-a1", "first run")
	appendHistoryRow(t, s, store.History{
		TaskID:    "proj-a1",
		Timestamp: "2026-04-18T01:00:00Z",
		Action:    store.HistoryCreated,
		Role:      "planner",
		Session:   "sess-1",
	})

	workspace := t.TempDir()
	if err, _, _ := runExport(t, s, exportCfg(workspace), nil); err != nil {
		t.Fatalf("first Export: %v", err)
	}
	first := snapshotTree(t, filepath.Join(workspace, "quest-export"))

	if err, _, _ := runExport(t, s, exportCfg(workspace), nil); err != nil {
		t.Fatalf("second Export: %v", err)
	}
	second := snapshotTree(t, filepath.Join(workspace, "quest-export"))

	if !equalStringMap(first, second) {
		t.Fatalf("export not idempotent: trees differ")
	}
}

// TestExportDeletesStaleFiles pins the phase note: re-running
// overwrites the output directory and deletes files for tasks that no
// longer exist (phase-11-export.md implementation notes — "Plan
// extends this to remove files for tasks that no longer exist").
func TestExportDeletesStaleFiles(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	seedMinimalTask(t, s, "proj-a2", "Beta")
	setDebrief(t, s, "proj-a2", "Beta debrief")

	workspace := t.TempDir()
	if err, _, _ := runExport(t, s, exportCfg(workspace), nil); err != nil {
		t.Fatalf("first Export: %v", err)
	}

	// Simulate a task being moved/deleted. We manually drop proj-a2
	// from the DB to avoid coupling this test to the move/cancel
	// handlers — the export path is what's under test.
	tx, txErr := s.BeginImmediate(context.Background(), store.TxCancel)
	if txErr != nil {
		t.Fatalf("BeginImmediate: %v", txErr)
	}
	if _, err := tx.ExecContext(context.Background(), "DELETE FROM tasks WHERE id='proj-a2'"); err != nil {
		t.Fatalf("delete proj-a2: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit delete: %v", err)
	}

	if err, _, _ := runExport(t, s, exportCfg(workspace), nil); err != nil {
		t.Fatalf("second Export: %v", err)
	}
	requireFile(t, filepath.Join(workspace, "quest-export", "tasks", "proj-a1.json"))
	requireNoFile(t, filepath.Join(workspace, "quest-export", "tasks", "proj-a2.json"))
	requireNoFile(t, filepath.Join(workspace, "quest-export", "debriefs", "proj-a2.md"))
}

// TestExportCustomDirFlag resolves --dir relative to CWD (standard CLI
// convention); the plan tightens only the default resolution to land
// beside .quest/.
func TestExportCustomDirFlag(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	workspace := t.TempDir()
	customDir := filepath.Join(workspace, "archives", "snapshot-1")
	err, stdout, _ := runExport(t, s, exportCfg(workspace), []string{"--dir", customDir})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var ack struct {
		Dir string `json:"dir"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v", jerr)
	}
	if ack.Dir != customDir {
		t.Errorf("ack.Dir = %q, want %q", ack.Dir, customDir)
	}
	requireFile(t, filepath.Join(customDir, "tasks", "proj-a1.json"))
}

// TestExportTextFormat emits the bare absolute path, following the
// `quest init` text-mode convention (spec is silent on export text
// mode — plan mirrors init for consistency).
func TestExportTextFormat(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	workspace := t.TempDir()
	cfg := exportCfg(workspace)
	cfg.Output.Format = "text"
	err, stdout, _ := runExport(t, s, cfg, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	want := filepath.Join(workspace, "quest-export")
	got := strings.TrimRight(stdout, "\n")
	if got != want {
		t.Errorf("text stdout = %q, want %q", got, want)
	}
}

// TestExportUnexpectedArgs rejects trailing positionals.
func TestExportUnexpectedArgs(t *testing.T) {
	s, _ := testStore(t)
	workspace := t.TempDir()
	err, _, _ := runExport(t, s, exportCfg(workspace), []string{"unexpected"})
	if err == nil {
		t.Fatalf("got nil err, want ErrUsage")
	}
}

// --- test helpers ---

func setDebrief(t *testing.T, s store.Store, id, debrief string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxComplete)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		"UPDATE tasks SET debrief = ? WHERE id = ?", debrief, id); err != nil {
		t.Fatalf("set debrief: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func appendHistoryRow(t *testing.T, s store.Store, h store.History) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if err := store.AppendHistory(context.Background(), tx, h); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func requireFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("expected file at %s, got directory", path)
	}
}

func requireNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected no file at %s, but it exists", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonEqual re-marshals both sides with encoding/json and compares the
// byte forms. Good enough to catch field-level divergence without a
// deep-equal helper.
func jsonEqual(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

func splitJSONL(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// snapshotTree reads every file under root and returns path→bytes as
// strings so two snapshots can be equality-compared. Directory entries
// are represented by an empty string value at the directory path.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if info.IsDir() {
			out["DIR:"+rel] = ""
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out["FILE:"+rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

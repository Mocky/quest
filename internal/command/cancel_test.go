//go:build integration

package command_test

import (
	"bytes"
	"context"
	"database/sql"
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

// runCancel mirrors the other runX helpers.
func runCancel(t *testing.T, s store.Store, cfg config.Config, stdin string, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Cancel(context.Background(), cfg, s, args, strings.NewReader(stdin), &out, &errb)
	return err, out.String(), errb.String()
}

// TestCancelOpenLeafHappyPath: a planner cancels an open leaf. Stdout
// carries {cancelled:[id], skipped:[]}; status flips; history lands.
func TestCancelOpenLeafHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	err, stdout, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var ack struct {
		Cancelled []string `json:"cancelled"`
		Skipped   []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"skipped"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if len(ack.Cancelled) != 1 || ack.Cancelled[0] != "proj-a1" {
		t.Errorf("cancelled = %v, want [proj-a1]", ack.Cancelled)
	}
	if len(ack.Skipped) != 0 {
		t.Errorf("skipped = %v, want []", ack.Skipped)
	}

	var status sql.NullString
	queryOne(t, dbPath, "SELECT status FROM tasks WHERE id='proj-a1'").Scan(&status)
	if status.String != "cancelled" {
		t.Errorf("status = %q, want cancelled", status.String)
	}

	var hcount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='cancelled'").Scan(&hcount)
	if hcount != 1 {
		t.Errorf("history cancelled count = %d, want 1", hcount)
	}
}

// TestCancelAcceptedLeafHappyPath: accepted → cancelled.
func TestCancelAcceptedLeafHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	err, _, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var status sql.NullString
	queryOne(t, dbPath, "SELECT status FROM tasks WHERE id='proj-a1'").Scan(&status)
	if status.String != "cancelled" {
		t.Errorf("status = %q, want cancelled", status.String)
	}
}

// TestCancelAlreadyCancelledIsIdempotent: a second cancel on a
// cancelled task exits 0 with empty arrays and does not write history.
func TestCancelAlreadyCancelledIsIdempotent(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "cancelled")

	err, stdout, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var ack struct {
		Cancelled []string `json:"cancelled"`
		Skipped   []any    `json:"skipped"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v", jerr)
	}
	if len(ack.Cancelled) != 0 {
		t.Errorf("cancelled = %v, want []", ack.Cancelled)
	}
	if len(ack.Skipped) != 0 {
		t.Errorf("skipped = %v, want []", ack.Skipped)
	}

	var hcount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='cancelled'").Scan(&hcount)
	if hcount != 0 {
		t.Errorf("idempotent cancel should not write history; got %d rows", hcount)
	}
}

// TestCancelTerminalStateRejected: completed / failed are permanent.
func TestCancelTerminalStateRejected(t *testing.T) {
	for _, from := range []string{"completed", "failed"} {
		t.Run(from, func(t *testing.T) {
			s, _ := testStore(t)
			seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", from)

			err, _, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1"})
			if err == nil {
				t.Fatalf("Cancel: got nil, want ErrConflict")
			}
			if !stderrors.Is(err, errors.ErrConflict) {
				t.Fatalf("err = %v, want wraps ErrConflict", err)
			}
		})
	}
}

// TestCancelNotFound: missing task → exit 3.
func TestCancelNotFound(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-nope"})
	if err == nil {
		t.Fatalf("Cancel: got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestCancelWithNonTerminalChildrenWithoutR: without -r, non-terminal
// children block the cancel and the root stays open.
func TestCancelWithNonTerminalChildrenWithoutR(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Parent", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.1", "Child", "proj-a1", "open")

	err, _, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1"})
	if err == nil {
		t.Fatalf("Cancel: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	var status sql.NullString
	queryOne(t, dbPath, "SELECT status FROM tasks WHERE id='proj-a1'").Scan(&status)
	if status.String != "open" {
		t.Errorf("parent status = %q, want unchanged open", status.String)
	}
}

// TestCancelRecursiveMultiLevel: -r on a 4-level tree transitions
// every non-terminal descendant to cancelled, reports already-terminal
// descendants in skipped, and orders `cancelled` with the target first
// followed by descendants in ID order.
func TestCancelRecursiveMultiLevel(t *testing.T) {
	s, dbPath := testStore(t)
	// depth 1: root
	seedTaskWithStatus(t, s, "proj-a1", "Root", "", "open")
	// depth 2
	seedTaskWithStatus(t, s, "proj-a1.1", "L2a", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.2", "L2b", "proj-a1", "accepted")
	seedTaskWithStatus(t, s, "proj-a1.3", "L2c-done", "proj-a1", "completed")
	// depth 3
	seedTaskWithStatus(t, s, "proj-a1.1.1", "L3a", "proj-a1.1", "open")
	seedTaskWithStatus(t, s, "proj-a1.2.1", "L3b-failed", "proj-a1.2", "failed")
	// depth 3 (below accepted parent)
	seedTaskWithStatus(t, s, "proj-a1.2.2", "L3c", "proj-a1.2", "open")

	err, stdout, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1", "-r"})
	if err != nil {
		t.Fatalf("Cancel -r: %v", err)
	}
	var ack struct {
		Cancelled []string `json:"cancelled"`
		Skipped   []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"skipped"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}

	wantCancelled := []string{"proj-a1", "proj-a1.1", "proj-a1.1.1", "proj-a1.2", "proj-a1.2.2"}
	if !equalStrings(ack.Cancelled, wantCancelled) {
		t.Errorf("cancelled = %v, want %v", ack.Cancelled, wantCancelled)
	}
	wantSkipped := map[string]string{
		"proj-a1.3":   "completed",
		"proj-a1.2.1": "failed",
	}
	if len(ack.Skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want %d entries", ack.Skipped, len(wantSkipped))
	}
	for _, s := range ack.Skipped {
		if wantSkipped[s.ID] != s.Status {
			t.Errorf("skipped entry %+v unexpected; want id->status %v", s, wantSkipped)
		}
	}

	// DB state: every ID in wantCancelled is now cancelled; skipped
	// entries keep their original status.
	for _, id := range wantCancelled {
		if got := lookupStatus(t, dbPath, id); got != "cancelled" {
			t.Errorf("status(%s) = %q, want cancelled", id, got)
		}
	}
	if got := lookupStatus(t, dbPath, "proj-a1.3"); got != "completed" {
		t.Errorf("skipped proj-a1.3 status = %q, want completed", got)
	}
	if got := lookupStatus(t, dbPath, "proj-a1.2.1"); got != "failed" {
		t.Errorf("skipped proj-a1.2.1 status = %q, want failed", got)
	}
}

// TestCancelRecursiveLeafProducesEmptySkipped: -r on a leaf is a normal
// single-task cancel; skipped stays empty.
func TestCancelRecursiveLeafProducesEmptySkipped(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	err, stdout, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1", "-r"})
	if err != nil {
		t.Fatalf("Cancel -r: %v", err)
	}
	var ack struct {
		Cancelled []string `json:"cancelled"`
		Skipped   []any    `json:"skipped"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v", jerr)
	}
	if len(ack.Cancelled) != 1 || ack.Cancelled[0] != "proj-a1" {
		t.Errorf("cancelled = %v, want [proj-a1]", ack.Cancelled)
	}
	if len(ack.Skipped) != 0 {
		t.Errorf("skipped = %v, want empty", ack.Skipped)
	}
}

// TestCancelWithReasonRecordsHistory: --reason persists into history
// payload.
func TestCancelWithReasonRecordsHistory(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	err, _, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1", "--reason", "superseded"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var payload string
	queryOne(t, dbPath, "SELECT payload FROM history WHERE task_id='proj-a1' AND action='cancelled'").Scan(&payload)
	var obj map[string]any
	if jerr := json.Unmarshal([]byte(payload), &obj); jerr != nil {
		t.Fatalf("payload JSON: %v; raw=%q", jerr, payload)
	}
	if obj["reason"] != "superseded" {
		t.Errorf("reason = %v, want superseded", obj["reason"])
	}
}

// TestCancelEmptyReasonIsNullInHistory: `--reason ""` must be
// equivalent to omitting the flag per spec §quest cancel.
func TestCancelEmptyReasonIsNullInHistory(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	err, _, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1", "--reason", ""})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var payload string
	queryOne(t, dbPath, "SELECT payload FROM history WHERE task_id='proj-a1' AND action='cancelled'").Scan(&payload)
	var obj map[string]any
	if jerr := json.Unmarshal([]byte(payload), &obj); jerr != nil {
		t.Fatalf("payload JSON: %v; raw=%q", jerr, payload)
	}
	if r, ok := obj["reason"]; !ok {
		t.Errorf("reason key missing; want present with null value")
	} else if r != nil {
		t.Errorf("reason = %v, want null", r)
	}
}

// TestCancelInflightWorkerCoordination: spec §In-flight worker
// coordination — after cancel, a worker's `quest update` on the task
// returns the structured conflict body with status=cancelled.
func TestCancelInflightWorkerCoordination(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	err, _, _ := runCancel(t, s, plannerCfg(), "", []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	err, stdout, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-a1", "--note", "still here"})
	if err == nil {
		t.Fatalf("Update on cancelled: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	var body struct {
		Error   string `json:"error"`
		Task    string `json:"task"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &body); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if body.Status != "cancelled" || body.Message == "" {
		t.Errorf("body = %+v, want cancelled coordination", body)
	}
}

// TestCancelMissingIDReturnsUsage: no positional task ID.
func TestCancelMissingIDReturnsUsage(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCancel(t, s, plannerCfg(), "", nil)
	if err == nil {
		t.Fatalf("Cancel: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestCancelTextModeRendersLines: --format text emits one `cancelled:
// <id>` line per cancelled task and one `skipped: <id> (<status>)` per
// skipped entry.
func TestCancelTextModeRendersLines(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Parent", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.1", "Child", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.2", "Done", "proj-a1", "completed")

	cfg := plannerCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runCancel(t, s, cfg, "", []string{"proj-a1", "-r"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !strings.Contains(stdout, "cancelled: proj-a1\n") {
		t.Errorf("stdout missing cancelled: proj-a1 line; got %q", stdout)
	}
	if !strings.Contains(stdout, "cancelled: proj-a1.1\n") {
		t.Errorf("stdout missing cancelled: proj-a1.1 line; got %q", stdout)
	}
	if !strings.Contains(stdout, "skipped: proj-a1.2 (completed)\n") {
		t.Errorf("stdout missing skipped line; got %q", stdout)
	}
}

// equalStrings compares string slices in order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lookupStatus reads tasks.status for id via a sibling connection.
func lookupStatus(t *testing.T, dbPath, id string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var st sql.NullString
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatalf("lookup %s: %v", id, err)
	}
	return st.String
}

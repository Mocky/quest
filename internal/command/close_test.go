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

// runComplete / runFail mirror the other runX helpers.
func runComplete(t *testing.T, s store.Store, cfg config.Config, stdin string, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Complete(context.Background(), cfg, s, args, strings.NewReader(stdin), &out, &errb)
	return err, out.String(), errb.String()
}

func runFail(t *testing.T, s store.Store, cfg config.Config, stdin string, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Fail(context.Background(), cfg, s, args, strings.NewReader(stdin), &out, &errb)
	return err, out.String(), errb.String()
}

// TestCompleteLeafHappyPath: a worker owning an accepted leaf calls
// complete. Status flips to complete, completed_at is recorded,
// debrief stored, history entry lands.
func TestCompleteLeafHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, stdout, _ := runComplete(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", "done"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var ack struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if ack.ID != "proj-a1" || ack.Status != "complete" {
		t.Errorf("ack = %+v, want {proj-a1, complete}", ack)
	}

	var status, completedAt, debrief sql.NullString
	queryOne(t, dbPath, "SELECT status, completed_at, debrief FROM tasks WHERE id='proj-a1'").
		Scan(&status, &completedAt, &debrief)
	if status.String != "complete" {
		t.Errorf("status = %v, want complete", status.String)
	}
	if completedAt.String == "" {
		t.Errorf("completed_at empty")
	}
	if debrief.String != "done" {
		t.Errorf("debrief = %q, want 'done'", debrief.String)
	}

	var count int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='completed'").Scan(&count)
	if count != 1 {
		t.Errorf("completed history = %d, want 1", count)
	}
}

// TestFailLeafHappyPath mirrors the complete case for fail.
func TestFailLeafHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, stdout, _ := runFail(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", "upstream unreachable"})
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	var ack struct {
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v", jerr)
	}
	if ack.Status != "failed" {
		t.Errorf("status = %q, want failed", ack.Status)
	}
	var status sql.NullString
	queryOne(t, dbPath, "SELECT status FROM tasks WHERE id='proj-a1'").Scan(&status)
	if status.String != "failed" {
		t.Errorf("db status = %q, want failed", status.String)
	}
}

// TestCompleteMissingDebriefReturnsUsage: no --debrief flag → exit 2.
func TestCompleteMissingDebriefReturnsUsage(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-owner"), "", []string{"proj-a1"})
	if err == nil {
		t.Fatalf("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestCompleteEmptyDebriefReturnsUsage: --debrief "" literal empty
// rejected. Runs after state checks so error class is usage only when
// state checks would have passed.
func TestCompleteEmptyDebriefReturnsUsage(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", ""})
	if err == nil {
		t.Fatalf("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestCompleteWhitespaceDebriefAccepted: per M10 decision, whitespace
// (space, tab, newline) is NOT the same as empty; store as-is.
func TestCompleteWhitespaceDebriefAccepted(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", "   "})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var debrief sql.NullString
	queryOne(t, dbPath, "SELECT debrief FROM tasks WHERE id='proj-a1'").Scan(&debrief)
	if debrief.String != "   " {
		t.Errorf("debrief = %q, want '   '", debrief.String)
	}
}

// TestCompleteFromNonOwningSessionReturnsExit4 pins the ownership
// check per spec §accept ("only the owning session (or an elevated
// role) can call complete/fail"). Second session attempts to close a
// task owned by the first.
func TestCompleteFromNonOwningSessionReturnsExit4(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-stranger"), "",
		[]string{"proj-a1", "--debrief", "sneaky"})
	if err == nil {
		t.Fatalf("got nil, want ErrPermission")
	}
	if !stderrors.Is(err, errors.ErrPermission) {
		t.Fatalf("err = %v, want wraps ErrPermission", err)
	}

	// Verify state is unchanged.
	var status sql.NullString
	queryOne(t, dbPath, "SELECT status FROM tasks WHERE id='proj-a1'").Scan(&status)
	if status.String != "accepted" {
		t.Errorf("status = %q, want unchanged accepted", status.String)
	}
}

// TestFailFromNonOwningSessionReturnsExit4 mirrors the complete case.
func TestFailFromNonOwningSessionReturnsExit4(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runFail(t, s, workerCfg("sess-stranger"), "",
		[]string{"proj-a1", "--debrief", "x"})
	if err == nil {
		t.Fatalf("got nil, want ErrPermission")
	}
	if !stderrors.Is(err, errors.ErrPermission) {
		t.Fatalf("err = %v, want wraps ErrPermission", err)
	}
}

// TestCompleteElevatedBypassesOwnership: an elevated role can close a
// task owned by another session (typical lead-override path).
func TestCompleteElevatedBypassesOwnership(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, plannerCfg(), "",
		[]string{"proj-a1", "--debrief", "ok"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

// TestCompleteOnOpenLeafReturnsExit5 pins C3: an elevated planner
// running `quest complete LEAF-ID --debrief "..."` on an open leaf
// is rejected with exit 5 and the leaf_direct_close carve-out.
func TestCompleteOnOpenLeafReturnsExit5(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runComplete(t, s, plannerCfg(), "",
		[]string{"proj-a1", "--debrief", "skip"})
	if err == nil {
		t.Fatalf("got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "leaf") {
		t.Errorf("err = %q, want mentions leaf", err.Error())
	}
}

// TestCompleteParentDirectCloseSucceeds: an elevated planner closes
// an open parent whose children are terminal.
func TestCompleteParentDirectCloseSucceeds(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Parent", "open", "")
	seedTaskWithStatus(t, s, "proj-a1.1", "Child", "proj-a1", "complete")

	err, stdout, _ := runComplete(t, s, plannerCfg(), "",
		[]string{"proj-a1", "--debrief", "verified inline"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(stdout, `"status":"complete"`) {
		t.Errorf("stdout = %q, want status=complete", stdout)
	}
}

// TestCompleteParentWithNonTerminalChildrenExit5 pins the shared body
// shape: children that are not complete/failed/cancelled block parent
// completion.
func TestCompleteParentWithNonTerminalChildrenExit5(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Parent", "accepted", "sess-owner")
	seedTaskWithStatus(t, s, "proj-a1.1", "Child-1", "proj-a1", "complete")
	seedTaskWithStatus(t, s, "proj-a1.2", "Child-2", "proj-a1", "accepted")

	err, stdout, _ := runComplete(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", "x"})
	if err == nil {
		t.Fatalf("got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	var body struct {
		Error               string `json:"error"`
		NonTerminalChildren []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"non_terminal_children"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &body); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if len(body.NonTerminalChildren) != 1 {
		t.Fatalf("children = %d, want 1", len(body.NonTerminalChildren))
	}
	if body.NonTerminalChildren[0].ID != "proj-a1.2" {
		t.Errorf("child id = %q, want proj-a1.2", body.NonTerminalChildren[0].ID)
	}
}

// TestCompleteFromTerminalStateExit5: a task already in complete /
// failed / cancelled cannot be re-closed.
func TestCompleteFromTerminalStateExit5(t *testing.T) {
	cases := []string{"complete", "failed"}
	for _, from := range cases {
		t.Run(from, func(t *testing.T) {
			s, _ := testStore(t)
			seedTaskFull(t, s, "proj-a1", "Alpha", from, "sess-owner")

			err, _, _ := runComplete(t, s, workerCfg("sess-owner"), "",
				[]string{"proj-a1", "--debrief", "x"})
			if err == nil {
				t.Fatalf("got nil, want ErrConflict")
			}
			if !stderrors.Is(err, errors.ErrConflict) {
				t.Fatalf("err = %v, want wraps ErrConflict", err)
			}
		})
	}
}

// TestCompleteFromCancelledEmitsStructuredBody: cancelled path emits
// the vigil coordination body plus exit 5.
func TestCompleteFromCancelledEmitsStructuredBody(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "cancelled", "sess-owner")

	err, stdout, _ := runComplete(t, s, plannerCfg(), "",
		[]string{"proj-a1", "--debrief", "x"})
	if err == nil {
		t.Fatalf("got nil, want ErrConflict")
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
	if body.Status != "cancelled" || body.Message != "task was cancelled" {
		t.Errorf("body = %+v, want cancelled coordination", body)
	}
}

// TestFailFromOpenReturnsConflict: fail doesn't accept open (only
// complete has the direct-close path).
func TestFailFromOpenReturnsConflict(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runFail(t, s, plannerCfg(), "",
		[]string{"proj-a1", "--debrief", "x"})
	if err == nil {
		t.Fatalf("got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "open status; accept first") {
		t.Errorf("err = %q, want 'accept first'", err.Error())
	}
}

// TestPrecondenceOrderingNotFoundBeforeEmptyDebrief: `complete
// nonexistent --debrief ""` must exit 3, not 2 — existence fires
// before usage (spec §Error precedence).
func TestPrecondenceOrderingNotFoundBeforeEmptyDebrief(t *testing.T) {
	s, _ := testStore(t)

	err, _, _ := runComplete(t, s, workerCfg("sess-x"), "",
		[]string{"proj-nope", "--debrief", ""})
	if err == nil {
		t.Fatalf("got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound (state before usage)", err)
	}
}

// TestPrecondenceOrderingOwnershipBeforeEmptyDebrief:
// `complete unowned-task --debrief ""` must exit 4 (non-owning
// worker), not 2 — ownership fires before usage.
func TestPrecondenceOrderingOwnershipBeforeEmptyDebrief(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-stranger"), "",
		[]string{"proj-a1", "--debrief", ""})
	if err == nil {
		t.Fatalf("got nil, want ErrPermission")
	}
	if !stderrors.Is(err, errors.ErrPermission) {
		t.Fatalf("err = %v, want wraps ErrPermission (state before usage)", err)
	}
}

// TestPrecondenceOrderingNotFoundBeforeMissingDebrief: `complete
// nonexistent` (no --debrief flag at all) must exit 3, not 2 —
// existence fires before usage even when --debrief is entirely
// absent (was: the nil check ran pre-tx and beat existence).
func TestPrecondenceOrderingNotFoundBeforeMissingDebrief(t *testing.T) {
	s, _ := testStore(t)

	err, _, _ := runComplete(t, s, workerCfg("sess-x"), "",
		[]string{"proj-nope"})
	if err == nil {
		t.Fatalf("got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound (state before usage)", err)
	}
}

// TestPrecondenceOrderingCancelledBeforeMissingDebrief: a worker
// calling `complete` on a cancelled task without --debrief must see
// exit 5 with the cancelledConflictBody (so vigil can terminate the
// worker), not exit 2. Regression for the pre-tx nil check that beat
// the state ladder.
func TestPrecondenceOrderingCancelledBeforeMissingDebrief(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "cancelled", "sess-owner")

	err, stdout, _ := runComplete(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1"})
	if err == nil {
		t.Fatalf("got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict (state before usage)", err)
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
	if body.Status != "cancelled" || body.Message != "task was cancelled" {
		t.Errorf("body = %+v, want cancelled coordination", body)
	}
}

// TestCompletePRAppendsAndHistory: complete with --pr appends the PR
// row and emits a pr_added history entry alongside the lifecycle row.
func TestCompletePRAppendsAndHistory(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", "ok", "--pr", "https://example/pr/1"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var prCount, historyCompleted, historyPR int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM prs WHERE task_id='proj-a1'").Scan(&prCount)
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='completed'").Scan(&historyCompleted)
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='pr_added'").Scan(&historyPR)
	if prCount != 1 || historyCompleted != 1 || historyPR != 1 {
		t.Errorf("counts = pr:%d completed:%d pr_added:%d, want 1/1/1",
			prCount, historyCompleted, historyPR)
	}
}

// TestCompleteDebriefViaStdinResolves: @-debrief reads stdin.
func TestCompleteDebriefViaStdinResolves(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-owner"), "stdin-based debrief",
		[]string{"proj-a1", "--debrief", "@-"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var debrief sql.NullString
	queryOne(t, dbPath, "SELECT debrief FROM tasks WHERE id='proj-a1'").Scan(&debrief)
	if debrief.String != "stdin-based debrief" {
		t.Errorf("debrief = %q, want stdin body", debrief.String)
	}
}

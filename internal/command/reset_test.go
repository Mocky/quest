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

func runReset(t *testing.T, s store.Store, cfg config.Config, stdin string, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Reset(context.Background(), cfg, s, args, strings.NewReader(stdin), &out, &errb)
	return err, out.String(), errb.String()
}

// seedAcceptedWithHandoff inserts an accepted task that has been
// handed off by its worker — mirrors the spec §Crash Recovery setup.
func seedAcceptedWithHandoff(t *testing.T, s store.Store, id, ownerSess, handoffBody, handoffSess, writtenAt string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, owner_session, started_at,
			handoff, handoff_session, handoff_written_at, created_at)
		 VALUES (?, ?, 'accepted', ?, ?, ?, ?, ?, ?)`,
		id, "Alpha", ownerSess, "2026-04-18T00:00:00Z",
		handoffBody, handoffSess, writtenAt, "2026-04-18T00:00:00Z")
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestResetHappyPath: accepted → open. owner_session / started_at
// cleared; status ack body returned; handoff preserved.
func TestResetHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedAcceptedWithHandoff(t, s, "proj-a1", "sess-w1",
		"stopped at step 3", "sess-w1", "2026-04-18T01:00:00Z")

	err, stdout, _ := runReset(t, s, plannerCfg(), "", []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	var ack struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if ack.ID != "proj-a1" || ack.Status != "open" {
		t.Errorf("ack = %+v, want {proj-a1, open}", ack)
	}

	var status, owner, startedAt, handoff, handoffSess, handoffWrittenAt sql.NullString
	queryOne(t, dbPath,
		`SELECT status, owner_session, started_at, handoff, handoff_session, handoff_written_at
		 FROM tasks WHERE id='proj-a1'`).
		Scan(&status, &owner, &startedAt, &handoff, &handoffSess, &handoffWrittenAt)
	if status.String != "open" {
		t.Errorf("status = %q, want open", status.String)
	}
	if owner.Valid {
		t.Errorf("owner_session = %q, want SQL NULL", owner.String)
	}
	if startedAt.Valid {
		t.Errorf("started_at = %q, want SQL NULL", startedAt.String)
	}
	if handoff.String != "stopped at step 3" {
		t.Errorf("handoff = %q, want preserved", handoff.String)
	}
	if handoffSess.String != "sess-w1" {
		t.Errorf("handoff_session = %q, want preserved", handoffSess.String)
	}
	if handoffWrittenAt.String != "2026-04-18T01:00:00Z" {
		t.Errorf("handoff_written_at = %q, want preserved", handoffWrittenAt.String)
	}

	var hcount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='reset'").Scan(&hcount)
	if hcount != 1 {
		t.Errorf("reset history count = %d, want 1", hcount)
	}
}

// TestResetNotAcceptedReturnsConflict covers every non-accepted from
// state per spec §quest reset: open, complete, failed, cancelled.
func TestResetNotAcceptedReturnsConflict(t *testing.T) {
	for _, from := range []string{"open", "complete", "failed", "cancelled"} {
		t.Run(from, func(t *testing.T) {
			s, _ := testStore(t)
			seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", from)

			err, _, _ := runReset(t, s, plannerCfg(), "", []string{"proj-a1"})
			if err == nil {
				t.Fatalf("Reset: got nil, want ErrConflict")
			}
			if !stderrors.Is(err, errors.ErrConflict) {
				t.Fatalf("err = %v, want wraps ErrConflict", err)
			}
		})
	}
}

// TestResetNotFound: missing task → exit 3.
func TestResetNotFound(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runReset(t, s, plannerCfg(), "", []string{"proj-nope"})
	if err == nil {
		t.Fatalf("Reset: got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestResetWithReasonRecordsHistory: --reason persists into history
// payload.
func TestResetWithReasonRecordsHistory(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	err, _, _ := runReset(t, s, plannerCfg(), "", []string{"proj-a1", "--reason", "worker crashed"})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	var payload string
	queryOne(t, dbPath, "SELECT payload FROM history WHERE task_id='proj-a1' AND action='reset'").Scan(&payload)
	var obj map[string]any
	if jerr := json.Unmarshal([]byte(payload), &obj); jerr != nil {
		t.Fatalf("payload JSON: %v; raw=%q", jerr, payload)
	}
	if obj["reason"] != "worker crashed" {
		t.Errorf("reason = %v, want 'worker crashed'", obj["reason"])
	}
}

// TestResetEmptyReasonIsNullInHistory pins the empty-reason rule —
// `--reason ""` is equivalent to omitting.
func TestResetEmptyReasonIsNullInHistory(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	err, _, _ := runReset(t, s, plannerCfg(), "", []string{"proj-a1", "--reason", ""})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	var payload string
	queryOne(t, dbPath, "SELECT payload FROM history WHERE task_id='proj-a1' AND action='reset'").Scan(&payload)
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

// TestResetCrashRecoveryRoundTrip is the Done-when scenario from spec
// §Crash Recovery: accept, handoff, reset, re-accept by a new session,
// the handoff is still visible on show.
func TestResetCrashRecoveryRoundTrip(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	// 1. Worker w1 accepts.
	if err, _, _ := runAccept(t, s, workerCfg("sess-w1"), []string{"proj-a1"}); err != nil {
		t.Fatalf("Accept w1: %v", err)
	}
	// 2. Worker w1 writes a handoff.
	if err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "",
		[]string{"proj-a1", "--handoff", "left at step 3"}); err != nil {
		t.Fatalf("Update handoff: %v", err)
	}
	// 3. Lead resets the task.
	if err, _, _ := runReset(t, s, plannerCfg(), "", []string{"proj-a1"}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	// 4. New worker w2 accepts.
	if err, _, _ := runAccept(t, s, workerCfg("sess-w2"), []string{"proj-a1"}); err != nil {
		t.Fatalf("Accept w2: %v", err)
	}
	// 5. Inspect the handoff via the DB — quest show is exercised
	//    elsewhere; this test focuses on the handoff surviving the
	//    reset/re-accept cycle.
	var handoff, handoffSess, owner, status sql.NullString
	queryOne(t, dbPath,
		`SELECT handoff, handoff_session, owner_session, status FROM tasks WHERE id='proj-a1'`).
		Scan(&handoff, &handoffSess, &owner, &status)
	if handoff.String != "left at step 3" {
		t.Errorf("handoff = %q, want preserved", handoff.String)
	}
	if handoffSess.String != "sess-w1" {
		t.Errorf("handoff_session = %q, want sess-w1 (original writer)", handoffSess.String)
	}
	if owner.String != "sess-w2" {
		t.Errorf("owner_session = %q, want sess-w2 (re-accept)", owner.String)
	}
	if status.String != "accepted" {
		t.Errorf("status = %q, want accepted", status.String)
	}
}

// TestResetMissingIDReturnsUsage: no positional ID, no AGENT_TASK
// default — reset is an elevated query-style command so it does not
// read AGENT_TASK.
func TestResetMissingIDReturnsUsage(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runReset(t, s, plannerCfg(), "", nil)
	if err == nil {
		t.Fatalf("Reset: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestResetTextModeRendersAck: --format text emits `<id> reset to open`.
func TestResetTextModeRendersAck(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	cfg := plannerCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runReset(t, s, cfg, "", []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if !strings.Contains(stdout, "proj-a1 reset to open") {
		t.Errorf("stdout = %q, want contains 'proj-a1 reset to open'", stdout)
	}
}

//go:build integration

package command_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// runUpdate mirrors runAccept for the update handler.
func runUpdate(t *testing.T, s store.Store, cfg config.Config, stdin string, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Update(context.Background(), cfg, s, args, strings.NewReader(stdin), &out, &errb)
	return err, out.String(), errb.String()
}

// seedTaskFull inserts a task with explicit column values for tests
// that need a specific pre-state. status defaults to "open" if empty.
func seedTaskFull(t *testing.T, s store.Store, id, title, status, ownerSession string) {
	t.Helper()
	if status == "" {
		status = "open"
	}
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	var ownerArg any = sql.NullString{}
	if ownerSession != "" {
		ownerArg = ownerSession
	}
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, owner_session, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, title, status, ownerArg, "2026-04-18T00:00:00Z")
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// plannerCfg is a cfg tuned for elevated tests: role=planner, roles
// list includes "planner", session set.
func plannerCfg() config.Config {
	return config.Config{
		Workspace: config.WorkspaceConfig{ElevatedRoles: []string{"planner"}},
		Agent:     config.AgentConfig{Role: "planner", Session: "sess-p1"},
		Output:    config.OutputConfig{Format: "json"},
	}
}

func workerCfg(session string) config.Config {
	return config.Config{
		Workspace: config.WorkspaceConfig{ElevatedRoles: []string{"planner"}},
		Agent:     config.AgentConfig{Role: "worker", Session: session},
		Output:    config.OutputConfig{Format: "json"},
	}
}

// TestUpdateNoteHappyPath pins the simplest worker path: --note on an
// open task. A notes row appears, a note_added history entry fires,
// and stdout carries the action-ack.
func TestUpdateNoteHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, stdout, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-a1", "--note", "started work"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	var ack struct {
		ID string `json:"id"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if ack.ID != "proj-a1" {
		t.Errorf("ack.id = %q, want proj-a1", ack.ID)
	}
	// Note row present.
	var body string
	queryOne(t, dbPath, "SELECT body FROM notes WHERE task_id='proj-a1'").Scan(&body)
	if body != "started work" {
		t.Errorf("note body = %q, want 'started work'", body)
	}
	// History row present.
	var count int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='note_added'").Scan(&count)
	if count != 1 {
		t.Errorf("note_added history = %d, want 1", count)
	}
}

// TestUpdateElevatedFlagByWorkerDenied — mixed-flag gate: a worker
// attempting --tier on an open task returns ErrRoleDenied (exit 6).
// Existence fires first (task exists), then the role gate denies.
func TestUpdateElevatedFlagByWorkerDenied(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-a1", "--tier", "T3"})
	if err == nil {
		t.Fatalf("Update: got nil, want ErrRoleDenied")
	}
	if !stderrors.Is(err, errors.ErrRoleDenied) {
		t.Fatalf("err = %v, want wraps ErrRoleDenied", err)
	}
}

// TestUpdateElevatedFlagByPlannerAllowed runs the same --tier update
// as a planner role and succeeds.
func TestUpdateElevatedFlagByPlannerAllowed(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, stdout, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--tier", "T3"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(stdout, `"id":"proj-a1"`) {
		t.Errorf("stdout = %q, want ack id", stdout)
	}
	var tier sql.NullString
	queryOne(t, dbPath, "SELECT tier FROM tasks WHERE id='proj-a1'").Scan(&tier)
	if tier.String != "T3" {
		t.Errorf("tier = %v, want T3", tier)
	}
}

// TestUpdateEmptyValueRejected pins every flag listed in spec §update
// as rejecting empty strings with exit 2. Table-driven — one case per
// flag.
func TestUpdateEmptyValueRejected(t *testing.T) {
	cases := []struct {
		flag string
		cfg  config.Config
	}{
		{"--note", workerCfg("sess-w1")},
		{"--handoff", workerCfg("sess-w1")},
		{"--title", plannerCfg()},
		{"--description", plannerCfg()},
		{"--context", plannerCfg()},
		{"--role", plannerCfg()},
		{"--acceptance-criteria", plannerCfg()},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			s, _ := testStore(t)
			seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")
			err, _, _ := runUpdate(t, s, tc.cfg, "", []string{"proj-a1", tc.flag, ""})
			if err == nil {
				t.Fatalf("got nil, want ErrUsage")
			}
			if !stderrors.Is(err, errors.ErrUsage) {
				t.Fatalf("err = %v, want wraps ErrUsage", err)
			}
			if !strings.Contains(err.Error(), tc.flag) {
				t.Errorf("err = %q, want flag name", err.Error())
			}
		})
	}
}

// TestUpdateMetaHappyPath: worker cannot set meta (elevated), but
// planner --meta foo=bar persists + history fires.
func TestUpdateMetaHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--meta", "foo=bar"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	var metaJSON string
	queryOne(t, dbPath, "SELECT metadata FROM tasks WHERE id='proj-a1'").Scan(&metaJSON)
	var m map[string]any
	if jerr := json.Unmarshal([]byte(metaJSON), &m); jerr != nil {
		t.Fatalf("metadata JSON: %v", jerr)
	}
	if m["foo"] != "bar" {
		t.Errorf("metadata.foo = %v, want bar", m["foo"])
	}
	var count int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='field_updated'").Scan(&count)
	if count != 1 {
		t.Errorf("field_updated history = %d, want 1", count)
	}
}

// TestUpdateMetaReadMergeWrite pins the spec idempotency rule: a new
// key is set, an existing key is overwritten, pre-existing keys not on
// this invocation are preserved.
func TestUpdateMetaReadMergeWrite(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	if err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--meta", "foo=v1", "--meta", "bar=v1"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Overwrite foo, leave bar alone, add baz.
	if err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--meta", "foo=v2", "--meta", "baz=v1"}); err != nil {
		t.Fatalf("second: %v", err)
	}

	var metaJSON string
	queryOne(t, dbPath, "SELECT metadata FROM tasks WHERE id='proj-a1'").Scan(&metaJSON)
	var m map[string]any
	json.Unmarshal([]byte(metaJSON), &m)
	if m["foo"] != "v2" {
		t.Errorf("foo = %v, want v2", m["foo"])
	}
	if m["bar"] != "v1" {
		t.Errorf("bar = %v, want v1 (should be preserved)", m["bar"])
	}
	if m["baz"] != "v1" {
		t.Errorf("baz = %v, want v1 (should be new)", m["baz"])
	}
}

// TestUpdateMetaShapeErrors rejects missing '=', empty key, empty value.
func TestUpdateMetaShapeErrors(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")
	cases := []struct {
		name string
		arg  string
	}{
		{"no-equals", "foo"},
		{"empty-key", "=bar"},
		{"empty-value", "foo="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--meta", tc.arg})
			if err == nil {
				t.Fatalf("got nil, want ErrUsage")
			}
			if !stderrors.Is(err, errors.ErrUsage) {
				t.Fatalf("err = %v, want wraps ErrUsage", err)
			}
		})
	}
}

// TestUpdateHandoffUpsert pins the handoff write path: handoff,
// handoff_session, handoff_written_at all set atomically and a
// handoff_set history row fires with the content payload.
func TestUpdateHandoffUpsert(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-a1", "--handoff", "pick up where I left off"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	var handoff, sess, writtenAt sql.NullString
	queryOne(t, dbPath, "SELECT handoff, handoff_session, handoff_written_at FROM tasks WHERE id='proj-a1'").
		Scan(&handoff, &sess, &writtenAt)
	if handoff.String != "pick up where I left off" {
		t.Errorf("handoff = %q, want text", handoff.String)
	}
	if sess.String != "sess-w1" {
		t.Errorf("handoff_session = %q, want sess-w1", sess.String)
	}
	if writtenAt.String == "" {
		t.Errorf("handoff_written_at empty, want RFC3339")
	}
	var payload string
	queryOne(t, dbPath, "SELECT payload FROM history WHERE task_id='proj-a1' AND action='handoff_set'").Scan(&payload)
	if !strings.Contains(payload, `"pick up where I left off"`) {
		t.Errorf("handoff_set payload = %q, want includes content", payload)
	}
}

// TestUpdatePRIdempotent adds a PR twice; the second call is a no-op
// with no new history row.
func TestUpdatePRIdempotent(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	for i := 0; i < 2; i++ {
		if err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-a1", "--pr", "https://example/pr/1"}); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	var prCount, historyCount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM prs WHERE task_id='proj-a1'").Scan(&prCount)
	if prCount != 1 {
		t.Errorf("prs = %d, want 1 (idempotent)", prCount)
	}
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='pr_added'").Scan(&historyCount)
	if historyCount != 1 {
		t.Errorf("pr_added history = %d, want 1", historyCount)
	}
}

// TestUpdateOwnershipCheckSkippedOnOpen: a worker other than owner
// (actually, with no accept yet) can --note on an open task.
func TestUpdateOwnershipCheckSkippedOnOpen(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, workerCfg("sess-stranger"), "", []string{"proj-a1", "--note", "hello"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// TestUpdateOwnershipCheckFiresOnAccepted: non-owning worker on an
// accepted task returns ErrPermission.
func TestUpdateOwnershipCheckFiresOnAccepted(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runUpdate(t, s, workerCfg("sess-stranger"), "", []string{"proj-a1", "--note", "sneaky"})
	if err == nil {
		t.Fatalf("got nil, want ErrPermission")
	}
	if !stderrors.Is(err, errors.ErrPermission) {
		t.Fatalf("err = %v, want wraps ErrPermission", err)
	}
}

// TestUpdateOwningWorkerOnAcceptedAllowed: the owner can --note after
// accept.
func TestUpdateOwningWorkerOnAcceptedAllowed(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runUpdate(t, s, workerCfg("sess-owner"), "", []string{"proj-a1", "--note", "ok"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// TestUpdateOwnershipCheckFiresOnTerminal: per spec §accept ("After
// acceptance, only the owning session ... can call quest update") the
// ownership check must cover post-accept statuses, not just accepted.
// A non-owning worker adding a --note to a complete task owned by a
// different session returns exit 4 (permission), not a silent success.
func TestUpdateOwnershipCheckFiresOnTerminal(t *testing.T) {
	cases := []string{"complete", "failed", "cancelled"}
	for _, status := range cases {
		t.Run(status, func(t *testing.T) {
			s, _ := testStore(t)
			seedTaskFull(t, s, "proj-a1", "Alpha", status, "sess-owner")

			err, _, _ := runUpdate(t, s, workerCfg("sess-stranger"), "", []string{"proj-a1", "--note", "sneaky"})
			if err == nil {
				t.Fatalf("got nil, want ErrPermission")
			}
			if !stderrors.Is(err, errors.ErrPermission) {
				t.Fatalf("err = %v, want wraps ErrPermission", err)
			}
		})
	}
}

// TestUpdateElevatedOnAcceptedBypassesOwnership: an elevated role can
// update an accepted task owned by a different session.
func TestUpdateElevatedOnAcceptedBypassesOwnership(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--tier", "T3"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// TestUpdateCancelledRejectsEverything pins the cancelled-state spec
// behavior: ALL flags rejected, stdout emits the coordination body.
func TestUpdateCancelledRejectsEverything(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "cancelled", "")

	cases := []struct {
		name string
		args []string
	}{
		{"note", []string{"--note", "hi"}},
		{"pr", []string{"--pr", "https://example/pr"}},
		{"handoff", []string{"--handoff", "ctx"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := append([]string{"proj-a1"}, tc.args...)
			err, stdout, _ := runUpdate(t, s, plannerCfg(), "", argv)
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
				t.Fatalf("body: %v; raw=%q", jerr, stdout)
			}
			if body.Status != "cancelled" || body.Message != "task was cancelled" {
				t.Errorf("body = %+v, want cancelled coordination", body)
			}
		})
	}
}

// TestUpdateTerminalStateAllowsNoteAndPR: --note/--pr/--meta survive
// on complete/failed tasks; other flags are rejected.
func TestUpdateTerminalStateAllowsNoteAndPR(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "complete", "sess-owner")

	// --note allowed.
	if err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--note", "post-mortem"}); err != nil {
		t.Errorf("--note on complete: %v", err)
	}
	// --title blocked.
	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--title", "new"})
	if err == nil {
		t.Fatalf("--title on complete: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "--title") {
		t.Errorf("err = %q, want blocked list", err.Error())
	}
}

// TestUpdateTypeTransitionBlocked: --type task blocked when outgoing
// caused-by exists on a bug-type task.
func TestUpdateTypeTransitionBlocked(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Root", "open", "")
	seedTaskFull(t, s, "proj-bug", "Bug", "open", "")
	// Add a caused-by link with proj-bug as the source (type change is blocked on source).
	tx, err := s.BeginImmediate(context.Background(), store.TxLink)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES ('proj-bug', 'proj-a1', 'caused-by', '2026-04-18T01:00:00Z')`)
	if err != nil {
		t.Fatalf("insert dep: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err, _, _ = runUpdate(t, s, plannerCfg(), "", []string{"proj-bug", "--type", "task"})
	if err == nil {
		t.Fatalf("got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "caused-by") {
		t.Errorf("err = %q, want mentions caused-by", err.Error())
	}
}

// TestUpdateTypeTransitionCheckBeforeUsageCheck pins the spec §Error
// precedence rule: --type task with outgoing caused-by link plus an
// empty --role still exits 5 (conflict), not 2 (usage) — state checks
// precede shape checks.
func TestUpdateTypeTransitionCheckBeforeUsageCheck(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Root", "open", "")
	seedTaskFull(t, s, "proj-bug", "Bug", "open", "")
	tx, err := s.BeginImmediate(context.Background(), store.TxLink)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES ('proj-bug', 'proj-a1', 'caused-by', '2026-04-18T01:00:00Z')`)
	if err != nil {
		t.Fatalf("insert dep: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err, _, _ = runUpdate(t, s, plannerCfg(), "", []string{"proj-bug", "--type", "task", "--role", ""})
	if err == nil {
		t.Fatalf("got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict (state before usage)", err)
	}
}

// TestUpdateInvalidTypeRejected: --type outside the spec enum (task,
// bug) exits 2 (usage) before any mutation. Mirrors the create-side
// enum check so one helper covers both commands.
func TestUpdateInvalidTypeRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Root", "open", "")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--type", "epic"})
	if err == nil {
		t.Fatalf("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestUpdateInvalidTierRejected: --tier outside T0..T6 exits 2.
func TestUpdateInvalidTierRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Root", "open", "")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--tier", "T9"})
	if err == nil {
		t.Fatalf("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestUpdateElevatedFieldHistoryCaptured pins the history payload
// shape: changing --title emits one field_updated row with
// fields.title = {from, to}.
func TestUpdateElevatedFieldHistoryCaptured(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--title", "Beta"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	var payload string
	queryOne(t, dbPath, "SELECT payload FROM history WHERE task_id='proj-a1' AND action='field_updated'").Scan(&payload)
	var p map[string]any
	if jerr := json.Unmarshal([]byte(payload), &p); jerr != nil {
		t.Fatalf("payload: %v", jerr)
	}
	fields, _ := p["fields"].(map[string]any)
	title, _ := fields["title"].(map[string]any)
	if title["from"] != "Alpha" || title["to"] != "Beta" {
		t.Errorf("payload.fields.title = %v, want {from: Alpha, to: Beta}", title)
	}
}

// TestUpdateHandoffViaStdinResolves wires the @- stdin path: a worker
// runs --handoff @- with a body on stdin, the resolver reads it, and
// the task's handoff column matches.
func TestUpdateHandoffViaStdinResolves(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "context from stdin", []string{"proj-a1", "--handoff", "@-"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	var handoff sql.NullString
	queryOne(t, dbPath, "SELECT handoff FROM tasks WHERE id='proj-a1'").Scan(&handoff)
	if handoff.String != "context from stdin" {
		t.Errorf("handoff = %q, want stdin body", handoff.String)
	}
}

// TestUpdateSecondStdinRejected: two @- args in one invocation exit 2
// with the second-stdin message.
func TestUpdateSecondStdinRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "body", []string{"proj-a1", "--handoff", "@-", "--note", "@-"})
	if err == nil {
		t.Fatalf("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "stdin already consumed") {
		t.Errorf("err = %q, want second-stdin message", err.Error())
	}
}

// TestUpdateFileResolves: --note @path reads a local file.
func TestUpdateFileResolves(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	path := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(path, []byte("file content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-a1", "--note", "@" + path})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	var body string
	queryOne(t, dbPath, "SELECT body FROM notes WHERE task_id='proj-a1'").Scan(&body)
	if body != "file content" {
		t.Errorf("note body = %q, want 'file content'", body)
	}
}

// TestUpdateNotFoundReturnsExit3 pins the existence check: a missing
// task wraps ErrNotFound.
func TestUpdateNotFoundReturnsExit3(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-nope", "--note", "hi"})
	if err == nil {
		t.Fatalf("got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

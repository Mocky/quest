//go:build integration

package command_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// runAccept mirrors runShow for the accept handler.
func runAccept(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Accept(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// seedTaskWithStatus inserts a task row at the given status so tests
// can exercise the non-open from-status paths without calling Accept
// first.
func seedTaskWithStatus(t *testing.T, s store.Store, id, title, parent, status string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	var parentArg any = sql.NullString{}
	if parent != "" {
		parentArg = parent
	}
	_, err = tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, parent, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, title, status, parentArg, "2026-04-18T00:00:00Z")
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestAcceptLeafHappyPath: a worker accepts an open leaf. Status flips
// to accepted, owner_session is set from AGENT_SESSION, started_at is
// populated, and stdout carries the action-ack.
func TestAcceptLeafHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	cfg := baseCfg()
	cfg.Agent.Role = "worker"
	cfg.Agent.Session = "sess-w1"

	err, stdout, _ := runAccept(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	var ack struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if ack.ID != "proj-a1" || ack.Status != "accepted" {
		t.Errorf("ack = %+v, want {proj-a1, accepted}", ack)
	}

	// Confirm DB state.
	var status, owner, startedAt sql.NullString
	row := queryOne(t, dbPath, "SELECT status, owner_session, started_at FROM tasks WHERE id='proj-a1'")
	if scanErr := row.Scan(&status, &owner, &startedAt); scanErr != nil {
		t.Fatalf("query: %v", scanErr)
	}
	if !status.Valid || status.String != "accepted" {
		t.Errorf("status = %v, want accepted", status)
	}
	if !owner.Valid || owner.String != "sess-w1" {
		t.Errorf("owner_session = %v, want sess-w1", owner)
	}
	if !startedAt.Valid || startedAt.String == "" {
		t.Errorf("started_at = %v, want non-empty", startedAt)
	}

	// History has an accepted row.
	var count int
	hrow := queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='accepted'")
	if scanErr := hrow.Scan(&count); scanErr != nil {
		t.Fatalf("history count: %v", scanErr)
	}
	if count != 1 {
		t.Errorf("history.accepted count = %d, want 1", count)
	}
}

// TestAcceptLeafAlreadyAcceptedReturnsConflict exercises the exit-5
// path on a non-open from-status with an empty stdout (accept does not
// emit the cancelled coordination body).
func TestAcceptLeafAlreadyAcceptedReturnsConflict(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "accepted")

	err, stdout, _ := runAccept(t, s, baseCfg(), []string{"proj-a1"})
	if err == nil {
		t.Fatalf("Accept: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on accept conflict", stdout)
	}
	if !strings.Contains(err.Error(), "not in open status") {
		t.Errorf("err = %q, want 'not in open status'", err.Error())
	}
}

// TestAcceptLeafNotFound pins the existence check — exit 3 when the
// task does not exist.
func TestAcceptLeafNotFound(t *testing.T) {
	s, _ := testStore(t)
	err, stdout, _ := runAccept(t, s, baseCfg(), []string{"proj-nope"})
	if err == nil {
		t.Fatalf("Accept: got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

// TestAcceptParentBlockedByNonTerminalChild emits the structured body
// on stdout with the non_terminal_children array.
func TestAcceptParentBlockedByNonTerminalChild(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Parent", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.1", "Child-1", "proj-a1", "completed")
	seedTaskWithStatus(t, s, "proj-a1.2", "Child-2", "proj-a1", "accepted") // non-terminal blocker
	seedTaskWithStatus(t, s, "proj-a1.3", "Child-3", "proj-a1", "open")     // non-terminal blocker

	err, stdout, _ := runAccept(t, s, baseCfg(), []string{"proj-a1"})
	if err == nil {
		t.Fatalf("Accept: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}

	var body struct {
		Error               string `json:"error"`
		Task                string `json:"task"`
		NonTerminalChildren []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"non_terminal_children"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &body); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if body.Error != "conflict" || body.Task != "proj-a1" {
		t.Errorf("body = %+v, want {conflict, proj-a1, ...}", body)
	}
	if len(body.NonTerminalChildren) != 2 {
		t.Fatalf("non_terminal_children = %d entries, want 2", len(body.NonTerminalChildren))
	}
	ids := []string{body.NonTerminalChildren[0].ID, body.NonTerminalChildren[1].ID}
	wantIDs := []string{"proj-a1.2", "proj-a1.3"}
	for i, got := range ids {
		if got != wantIDs[i] {
			t.Errorf("non_terminal_children[%d].id = %q, want %q", i, got, wantIDs[i])
		}
	}
}

// TestAcceptParentWithTerminalChildrenSucceeds proves the verifier
// path: all children in terminal state → parent accept transitions.
func TestAcceptParentWithTerminalChildrenSucceeds(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Parent", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.1", "Child-1", "proj-a1", "completed")
	seedTaskWithStatus(t, s, "proj-a1.2", "Child-2", "proj-a1", "failed")
	seedTaskWithStatus(t, s, "proj-a1.3", "Child-3", "proj-a1", "cancelled")

	cfg := baseCfg()
	cfg.Agent.Role = "verifier"
	cfg.Agent.Session = "sess-v1"

	err, stdout, _ := runAccept(t, s, cfg, []string{"proj-a1"})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	var ack struct {
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("JSON: %v", jerr)
	}
	if ack.Status != "accepted" {
		t.Errorf("status = %q, want accepted", ack.Status)
	}
}

// TestAcceptMissingIDReturnsUsage enforces the usage-error result
// when the caller passes no positional task ID.
func TestAcceptMissingIDReturnsUsage(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runAccept(t, s, baseCfg(), nil)
	if err == nil {
		t.Fatalf("Accept: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestAcceptIgnoresAgentTaskWhenIDMissing pins the regression: worker
// commands do not fall back to AGENT_TASK when the positional ID is
// omitted. A future refactor that re-introduces the fallback fails
// this test.
func TestAcceptIgnoresAgentTaskWhenIDMissing(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	cfg := baseCfg()
	cfg.Agent.Task = "proj-a1"
	cfg.Agent.Session = "sess-w1"
	err, _, _ := runAccept(t, s, cfg, nil)
	if err == nil {
		t.Fatalf("Accept: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestAcceptUnsetAgentSessionPersistsAsNull confirms the nullable TEXT
// rule: AGENT_SESSION="" writes SQL NULL for owner_session, not "".
func TestAcceptUnsetAgentSessionPersistsAsNull(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	cfg := baseCfg()
	// Agent.Session intentionally empty.
	cfg.Agent.Role = "worker"

	if err, _, _ := runAccept(t, s, cfg, []string{"proj-a1"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	var owner sql.NullString
	row := queryOne(t, dbPath, "SELECT owner_session FROM tasks WHERE id='proj-a1'")
	if scanErr := row.Scan(&owner); scanErr != nil {
		t.Fatalf("query: %v", scanErr)
	}
	if owner.Valid {
		t.Errorf("owner_session = %q, want SQL NULL", owner.String)
	}
}

// TestAcceptHelpShortCircuits pins the STANDARDS.md §`--help` Convention
// for accept. The previous code rejected any non-positional residue
// before flag parsing, so `accept TASK --help` exited 2 with
// "unexpected arguments" even on destructive-adjacent commands where
// --help should be safe to probe. Help must print usage to stderr,
// leave stdout empty, exit 0, and never touch the DB.
func TestAcceptHelpShortCircuits(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"help alone", []string{"--help"}},
		{"help after ID", []string{"proj-a1", "--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := testStore(t)
			seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

			cfg := baseCfg()
			cfg.Agent.Role = "worker"
			cfg.Agent.Session = "sess-w1"

			err, stdout, stderr := runAccept(t, s, cfg, tc.args)
			if err != nil {
				t.Fatalf("Accept: err = %v, want nil", err)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "Usage of accept") {
				t.Errorf("stderr missing usage text; got %q", stderr)
			}
			// DB invariant: task must still be open — --help must not
			// mutate state.
			var status sql.NullString
			row := queryOne(t, dbPath, "SELECT status FROM tasks WHERE id='proj-a1'")
			if scanErr := row.Scan(&status); scanErr != nil {
				t.Fatalf("query: %v", scanErr)
			}
			if status.String != "open" {
				t.Errorf("status = %q, want open (--help must not mutate)", status.String)
			}
		})
	}
}

// TestConcurrentAcceptLeavesOnlyOneWinner matches TESTING.md
// §Concurrency Tests: 10 goroutines race on a single open task; the
// first writer wins, the rest observe exit-5 conflict. None silently
// succeed or return transient errors.
func TestConcurrentAcceptLeavesOnlyOneWinner(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Alpha", "", "open")

	const N = 10
	cfg := baseCfg()
	cfg.Agent.Role = "worker"

	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			local := cfg
			local.Agent.Session = "sess-" + string(rune('a'+i))
			var out, errb bytes.Buffer
			err := command.Accept(context.Background(), local,
				s, []string{"proj-a1"}, strings.NewReader(""), &out, &errb)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)

	var wins, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			wins++
		case stderrors.Is(err, errors.ErrConflict):
			conflicts++
		default:
			t.Errorf("unexpected err: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("wins = %d, want 1", wins)
	}
	if conflicts != N-1 {
		t.Errorf("conflicts = %d, want %d", conflicts, N-1)
	}
}

// queryOne runs a SELECT against the live DB via a sibling *sql.DB —
// the Store interface deliberately hides the raw handle so tests that
// need direct SQL open their own. Pass the path from testStore so
// both connections point at the same file.
func queryOne(t *testing.T, dbPath, q string) *sql.Row {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.QueryRow(q)
}

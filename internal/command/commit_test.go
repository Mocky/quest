//go:build integration

package command_test

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// TestUpdateCommitHappyPath: --commit BRANCH@HASH inserts one commit
// row plus one commit_added history entry with branch and hash as
// separate payload fields.
func TestUpdateCommitHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "",
		[]string{"proj-a1", "--commit", "master@abc1234"})
	if err != nil {
		t.Fatalf("Update --commit: %v", err)
	}

	var branch, hash, addedAt string
	queryOne(t, dbPath,
		"SELECT branch, hash, added_at FROM commits WHERE task_id='proj-a1'").
		Scan(&branch, &hash, &addedAt)
	if branch != "master" || hash != "abc1234" {
		t.Errorf("commit row = (%q, %q), want (master, abc1234)", branch, hash)
	}
	if addedAt == "" {
		t.Errorf("added_at empty")
	}

	var payload string
	queryOne(t, dbPath,
		"SELECT payload FROM history WHERE task_id='proj-a1' AND action='commit_added'").
		Scan(&payload)
	if !strings.Contains(payload, `"branch":"master"`) {
		t.Errorf("payload = %q, want branch=master", payload)
	}
	if !strings.Contains(payload, `"hash":"abc1234"`) {
		t.Errorf("payload = %q, want hash=abc1234", payload)
	}
}

// TestUpdateCommitRepeatable: multiple --commit flags in one call
// produce multiple commit rows (same-invocation idempotency still
// swallows exact duplicates).
func TestUpdateCommitRepeatable(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "",
		[]string{"proj-a1",
			"--commit", "master@abc1234",
			"--commit", "feature/x@deadbeef",
			"--commit", "master@abc1234", // duplicate of first
		})
	if err != nil {
		t.Fatalf("Update --commit: %v", err)
	}

	var count, historyCount int
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&count)
	if count != 2 {
		t.Errorf("commits count = %d, want 2 (duplicate dropped)", count)
	}
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='commit_added'").
		Scan(&historyCount)
	if historyCount != 2 {
		t.Errorf("commit_added history = %d, want 2", historyCount)
	}
}

// TestUpdateCommitIdempotentAcrossCalls: two update calls with the
// same BRANCH@HASH leave one row and one history entry — parallel to
// TestUpdatePRIdempotent for --pr.
func TestUpdateCommitIdempotentAcrossCalls(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	for i := 0; i < 2; i++ {
		if err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "",
			[]string{"proj-a1", "--commit", "master@abc1234"}); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	var count, historyCount int
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&count)
	if count != 1 {
		t.Errorf("commits = %d, want 1 (idempotent)", count)
	}
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='commit_added'").
		Scan(&historyCount)
	if historyCount != 1 {
		t.Errorf("commit_added history = %d, want 1", historyCount)
	}
}

// TestUpdateCommitInvalidShapeExitsTwo: --commit with an invalid
// BRANCH@HASH value returns exit 2 (usage) before any DB I/O. Table-
// driven so every spec §Commit reference format rejection rule fires.
func TestUpdateCommitInvalidShapeExitsTwo(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"empty-hash", "master@"},
		{"empty-branch", "@abc1234"},
		{"no-separator", "abc1234"},
		{"hash-too-short", "master@abc"},
		{"uppercase-hash", "master@ABC1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := testStore(t)
			seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")
			err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "",
				[]string{"proj-a1", "--commit", tc.value})
			if err == nil {
				t.Fatalf("got nil, want ErrUsage")
			}
			if !stderrors.Is(err, errors.ErrUsage) {
				t.Fatalf("err = %v, want wraps ErrUsage", err)
			}
			// No row should have been inserted.
			var count int
			queryOne(t, dbPath,
				"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&count)
			if count != 0 {
				t.Errorf("commits = %d, want 0 (parse fails before DB I/O)", count)
			}
		})
	}
}

// TestUpdateCommitSplitsOnLastAt: a branch name containing '@' parses
// correctly — the parser splits on the rightmost '@'.
func TestUpdateCommitSplitsOnLastAt(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "",
		[]string{"proj-a1", "--commit", "release@2025@abc1234"})
	if err != nil {
		t.Fatalf("Update --commit: %v", err)
	}
	var branch, hash string
	queryOne(t, dbPath,
		"SELECT branch, hash FROM commits WHERE task_id='proj-a1'").
		Scan(&branch, &hash)
	if branch != "release@2025" {
		t.Errorf("branch = %q, want release@2025", branch)
	}
	if hash != "abc1234" {
		t.Errorf("hash = %q, want abc1234", hash)
	}
}

// TestCompleteCommitAppendsAndHistory: complete with --commit appends
// the commit row and emits a commit_added history entry alongside the
// completed row. Mirrors TestCompletePRAppendsAndHistory.
func TestCompleteCommitAppendsAndHistory(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runComplete(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", "ok", "--commit", "master@abc1234"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var commitCount, historyCommit int
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&commitCount)
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='commit_added'").
		Scan(&historyCommit)
	if commitCount != 1 || historyCommit != 1 {
		t.Errorf("counts = commits:%d commit_added:%d, want 1/1",
			commitCount, historyCommit)
	}
}

// TestFailCommitAcceptedWithDebrief: fail with --commit appends the
// commit row under the same rules as complete.
func TestFailCommitAcceptedWithDebrief(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-owner")

	err, _, _ := runFail(t, s, workerCfg("sess-owner"), "",
		[]string{"proj-a1", "--debrief", "couldn't finish", "--commit", "main@deadbeef"})
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	var count int
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&count)
	if count != 1 {
		t.Errorf("commits = %d, want 1", count)
	}
}

// TestUpdateCommitOnCompletedTaskAllowed: --commit is an append/
// annotation flag allowed on terminal (completed/failed) tasks per
// spec §update *Terminal-state gating*. Covers the update path; the
// complete/fail paths handle their own --commit at terminal transition.
func TestUpdateCommitOnCompletedTaskAllowed(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "completed", "sess-owner")

	err, _, _ := runUpdate(t, s, plannerCfg(), "",
		[]string{"proj-a1", "--commit", "master@abc1234"})
	if err != nil {
		t.Fatalf("Update --commit on completed: %v", err)
	}
	var count int
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&count)
	if count != 1 {
		t.Errorf("commits = %d, want 1", count)
	}
}

// TestUpdateCommitOnFailedTaskAllowed mirrors the completed case for
// the failed terminal state.
func TestUpdateCommitOnFailedTaskAllowed(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "failed", "sess-owner")

	err, _, _ := runUpdate(t, s, plannerCfg(), "",
		[]string{"proj-a1", "--commit", "master@abc1234"})
	if err != nil {
		t.Fatalf("Update --commit on failed: %v", err)
	}
	var count int
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&count)
	if count != 1 {
		t.Errorf("commits = %d, want 1", count)
	}
}

// TestShowEmitsCommitsArray: the commits field is always present in
// the JSON output, empty array when none — contract pinned in the
// required-fields list.
func TestShowEmitsCommitsArray(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	err, stdout, _ := runHandler(t, command.Show, s, baseCfg(),
		[]string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(stdout, `"commits":[]`) {
		t.Errorf("stdout = %q, want contains `\"commits\":[]`", stdout)
	}
}

// TestShowCommitsSectionInText: quest show --text renders a Commits
// section when the task carries commit rows.
func TestShowCommitsSectionInText(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO commits(task_id, branch, hash, added_at) VALUES
			('proj-a1', 'master', 'abc1234', '2026-04-18T10:30:00Z')`); err != nil {
		t.Fatalf("insert commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cfg := baseCfg()
	cfg.Output.Text = true
	err, stdout, _ := runHandler(t, command.Show, s, cfg, []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(stdout, "Commits") {
		t.Errorf("stdout missing Commits section:\n%s", stdout)
	}
	if !strings.Contains(stdout, "master@abc1234") {
		t.Errorf("stdout missing branch@hash:\n%s", stdout)
	}
}

// TestUpdateCommitLowercasedOnWrite: the parser rejects uppercase
// hashes, so the only way a case variant reaches the store is through
// a direct INSERT. The dedup UNIQUE index uses lower(hash), so a
// direct insert of "ABC123" (8+ chars to dodge the min-len rule) is
// treated as identical to "abc123" by the app's write path.
func TestUpdateCommitUniqueIndexCaseInsensitive(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "accepted", "sess-w1")

	// Out-of-band insert with mixed case — bypasses the parser, exercises
	// the UNIQUE (task_id, branch, lower(hash)) index.
	tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO commits(task_id, branch, hash, added_at) VALUES
			('proj-a1', 'master', 'ABC12345', '2026-04-18T10:30:00Z')`); err != nil {
		t.Fatalf("insert upper hash: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Now the app-path --commit with the lowercase equivalent must
	// be treated as a duplicate by the UNIQUE index.
	err, _, _ = runUpdate(t, s, workerCfg("sess-w1"), "",
		[]string{"proj-a1", "--commit", "master@abc12345"})
	if err != nil {
		t.Fatalf("Update --commit: %v", err)
	}

	var count int
	queryOne(t, dbPath,
		"SELECT COUNT(*) FROM commits WHERE task_id='proj-a1'").Scan(&count)
	if count != 1 {
		t.Errorf("commits = %d, want 1 (case-insensitive dedup)", count)
	}
}

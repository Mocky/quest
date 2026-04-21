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

func runMove(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Move(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// seedDep inserts a dependency edge source -> target with link_type.
func seedDep(t *testing.T, s store.Store, source, target, linkType string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO dependencies(task_id, target_id, link_type, created_at)
		 VALUES (?, ?, ?, ?)`,
		source, target, linkType, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert dep %s->%s: %v", source, target, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// seedTag inserts a tag row for id.
func seedTag(t *testing.T, s store.Store, id, tag string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tags(task_id, tag) VALUES (?, ?)`, id, tag); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// seedPR inserts a PR row for id.
func seedPR(t *testing.T, s store.Store, id, url string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO prs(task_id, url, added_at) VALUES (?, ?, ?)`,
		id, url, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert pr: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// seedNote inserts a note row for id.
func seedNote(t *testing.T, s store.Store, id, body string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO notes(task_id, timestamp, body) VALUES (?, ?, ?)`,
		id, "2026-04-18T00:00:00Z", body); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestMoveLeafHappyPath: a single-task move from under one parent to
// another. Subgraph size = 1, new root id is the next sub-task slot
// under the new parent.
func TestMoveLeafHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "Leaf", "proj-a1", "open")

	err, stdout, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-b2"})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	var ack struct {
		ID      string `json:"id"`
		Renames []struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"renames"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if ack.ID != "proj-b2.1" {
		t.Errorf("id = %q, want proj-b2.1", ack.ID)
	}
	if len(ack.Renames) != 1 || ack.Renames[0].Old != "proj-a1.3" || ack.Renames[0].New != "proj-b2.1" {
		t.Errorf("renames = %+v, want [{proj-a1.3, proj-b2.1}]", ack.Renames)
	}

	// Row present under new id, gone under old id.
	if got := lookupStatus(t, dbPath, "proj-b2.1"); got != "open" {
		t.Errorf("new id status = %q, want open", got)
	}
	var any int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM tasks WHERE id='proj-a1.3'").Scan(&any)
	if any != 0 {
		t.Errorf("old id still present: %d rows", any)
	}

	// History row landed on the new id.
	var hcount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-b2.1' AND action='moved'").Scan(&hcount)
	if hcount != 1 {
		t.Errorf("history moved rows = %d, want 1", hcount)
	}
}

// TestMoveSubgraphRoundTrip: a 3-level subgraph with tags, PRs, notes,
// history, and incoming blocked-by edges from outside moves cleanly;
// every affected task reflects the new IDs via its side tables. This
// is also the spec §quest move "Done when" scenario.
func TestMoveSubgraphRoundTrip(t *testing.T) {
	s, dbPath := testStore(t)
	// Donor tree.
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "SubRoot", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.3.1", "SubL2a", "proj-a1.3", "open")
	seedTaskWithStatus(t, s, "proj-a1.3.2", "SubL2b", "proj-a1.3", "open")
	// Adopter root.
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "open")
	// External task with blocked-by edge into the subgraph.
	seedTaskWithStatus(t, s, "proj-c1", "Outsider", "", "open")
	seedDep(t, s, "proj-c1", "proj-a1.3.1", "blocked-by")
	// Subgraph side data.
	seedTag(t, s, "proj-a1.3", "go")
	seedPR(t, s, "proj-a1.3.2", "https://example/pr/77")
	seedNote(t, s, "proj-a1.3.1", "initial note")

	err, stdout, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-b2"})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	var ack struct {
		ID      string `json:"id"`
		Renames []struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"renames"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if ack.ID != "proj-b2.1" {
		t.Errorf("id = %q, want proj-b2.1", ack.ID)
	}
	if len(ack.Renames) != 3 {
		t.Fatalf("renames = %d, want 3", len(ack.Renames))
	}

	// Renames ordered by old ID ascending.
	wantRenames := []struct{ Old, New string }{
		{"proj-a1.3", "proj-b2.1"},
		{"proj-a1.3.1", "proj-b2.1.1"},
		{"proj-a1.3.2", "proj-b2.1.2"},
	}
	for i, got := range ack.Renames {
		if got.Old != wantRenames[i].Old || got.New != wantRenames[i].New {
			t.Errorf("renames[%d] = %+v, want %+v", i, got, wantRenames[i])
		}
	}

	// Every moved task exists under its new ID.
	for _, r := range wantRenames {
		if got := lookupStatus(t, dbPath, r.New); got != "open" {
			t.Errorf("status(%s) = %q, want open", r.New, got)
		}
		var n int
		queryOne(t, dbPath, "SELECT COUNT(*) FROM tasks WHERE id='"+r.Old+"'").Scan(&n)
		if n != 0 {
			t.Errorf("old id %s still present", r.Old)
		}
	}

	// Tag cascaded.
	var tagCount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM tags WHERE task_id='proj-b2.1' AND tag='go'").Scan(&tagCount)
	if tagCount != 1 {
		t.Errorf("tag on renamed root = %d, want 1", tagCount)
	}

	// PR cascaded.
	var prCount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM prs WHERE task_id='proj-b2.1.2'").Scan(&prCount)
	if prCount != 1 {
		t.Errorf("pr on renamed leaf = %d, want 1", prCount)
	}

	// Note cascaded.
	var noteCount int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM notes WHERE task_id='proj-b2.1.1'").Scan(&noteCount)
	if noteCount != 1 {
		t.Errorf("note on renamed task = %d, want 1", noteCount)
	}

	// Incoming dependency edge updated (proj-c1 → proj-b2.1.1).
	var depTarget sql.NullString
	queryOne(t, dbPath, "SELECT target_id FROM dependencies WHERE task_id='proj-c1'").Scan(&depTarget)
	if depTarget.String != "proj-b2.1.1" {
		t.Errorf("dep target = %q, want proj-b2.1.1", depTarget.String)
	}

	// Parent pointers correctly rewritten.
	var parent sql.NullString
	queryOne(t, dbPath, "SELECT parent FROM tasks WHERE id='proj-b2.1'").Scan(&parent)
	if parent.String != "proj-b2" {
		t.Errorf("root parent = %q, want proj-b2", parent.String)
	}
	queryOne(t, dbPath, "SELECT parent FROM tasks WHERE id='proj-b2.1.1'").Scan(&parent)
	if parent.String != "proj-b2.1" {
		t.Errorf("descendant parent = %q, want proj-b2.1", parent.String)
	}
}

// TestMoveSubgraphFKIntegrity pins the H15 invariant: after a move
// that spans history, dependencies, tags, PRs, and notes, PRAGMA
// foreign_key_check returns zero violations. Doubles as the
// correctness proof for not using defer_foreign_keys.
func TestMoveSubgraphFKIntegrity(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "SubRoot", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.3.1", "SubL2", "proj-a1.3", "open")
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "open")
	// blocked-by edge from outside + inside the subgraph
	seedTaskWithStatus(t, s, "proj-c1", "Outsider", "", "open")
	seedDep(t, s, "proj-c1", "proj-a1.3", "blocked-by")
	seedDep(t, s, "proj-a1.3.1", "proj-a1.3", "blocked-by") // internal edge
	seedTag(t, s, "proj-a1.3", "go")
	seedPR(t, s, "proj-a1.3.1", "https://example/pr/1")
	seedNote(t, s, "proj-a1.3.1", "note body")

	if err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-b2"}); err != nil {
		t.Fatalf("Move: %v", err)
	}

	// Run foreign_key_check — must return zero rows.
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable foreign_keys: %v", err)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var tbl, rowid, parent, fkid sql.NullString
		_ = rows.Scan(&tbl, &rowid, &parent, &fkid)
		t.Fatalf("foreign_key_check returned rows; first: table=%q rowid=%q parent=%q fkid=%q",
			tbl.String, rowid.String, parent.String, fkid.String)
	}
}

// TestMoveRejectsAcceptedHistory pins the "any accepted action in
// history blocks the move" rule. A descendant that was accepted and
// reset still blocks.
func TestMoveRejectsAcceptedHistory(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "SubRoot", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.3.1", "SubL2", "proj-a1.3", "open")
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "open")
	// Simulate an accepted history row for the descendant (then reset).
	ctx := context.Background()
	tx, err := s.BeginImmediate(ctx, store.TxCreate)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if herr := store.AppendHistory(ctx, tx, store.History{
		TaskID:    "proj-a1.3.1",
		Timestamp: "2026-04-18T00:00:00Z",
		Action:    store.HistoryAccepted,
	}); herr != nil {
		t.Fatalf("append history: %v", herr)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err, _, _ = runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-b2"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "proj-a1.3.1") {
		t.Errorf("err = %q, want mentions proj-a1.3.1", err.Error())
	}
}

// TestMoveRejectsCurrentParentAccepted pins the "current parent not in
// accepted status" rule — a verifier is mid-flight on the parent.
func TestMoveRejectsCurrentParentAccepted(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "A1", "accepted", "sess-verify")
	seedTaskWithStatus(t, s, "proj-a1.3", "SubRoot", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "open")

	err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-b2"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "current parent") {
		t.Errorf("err = %q, want mentions current parent", err.Error())
	}
}

// TestMoveRejectsNewParentNotOpen pins the spec §quest move rule that
// NEW_PARENT must be in open status.
func TestMoveRejectsNewParentNotOpen(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "Sub", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "completed")

	err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-b2"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "not in open status") {
		t.Errorf("err = %q, want mentions open status", err.Error())
	}
}

// TestMoveRejectsCircularParentage pins the spec's "circular parent-
// child relationship" check. Moving a task under itself or its
// descendants must fail.
func TestMoveRejectsCircularParentage(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "SubRoot", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.3.1", "SubL2", "proj-a1.3", "open")

	// Target is a descendant of the moved task.
	err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-a1.3.1"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("err = %q, want mentions circular", err.Error())
	}

	// Target is the moved task itself.
	err, _, _ = runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-a1.3"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
}

// TestMoveRejectsDepthExceeded pins the MaxDepth=3 rule. A
// depth-3 task moved to a depth-3 parent would place it at depth 4.
func TestMoveRejectsDepthExceeded(t *testing.T) {
	s, _ := testStore(t)
	// proj-a1 (1) > proj-a1.1 (2) > proj-a1.1.1 (3) — depth-3 leaf
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.1", "L2", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.1.1", "L3", "proj-a1.1", "open")
	// proj-b1 (1) > proj-b1.1 (2) > proj-b1.1.1 (3) — depth-3 parent
	seedTaskWithStatus(t, s, "proj-b1", "B1", "", "open")
	seedTaskWithStatus(t, s, "proj-b1.1", "BL2", "proj-b1", "open")
	seedTaskWithStatus(t, s, "proj-b1.1.1", "BL3", "proj-b1.1", "open")

	// Moving a depth-2 task under a depth-3 parent would produce a
	// depth-4 root, exceeding MaxDepth.
	err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.1", "--parent", "proj-b1.1.1"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("err = %q, want mentions depth", err.Error())
	}
}

// TestMoveMissingTaskReturnsNotFound: existence check for the moved
// task.
func TestMoveMissingTaskReturnsNotFound(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "open")

	err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-nope", "--parent", "proj-b2"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestMoveMissingNewParentReturnsNotFound: existence check for the new
// parent.
func TestMoveMissingNewParentReturnsNotFound(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "Sub", "proj-a1", "open")

	err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3", "--parent", "proj-zzz"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrNotFound")
	}
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestMoveMissingParentFlagReturnsUsage: --parent is required.
func TestMoveMissingParentFlagReturnsUsage(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "Sub", "proj-a1", "open")

	err, _, _ := runMove(t, s, plannerCfg(), []string{"proj-a1.3"})
	if err == nil {
		t.Fatalf("Move: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestMoveTextModeRendersRenames emits `OLD → NEW` lines, one per
// rename, in old-ID order.
func TestMoveTextModeRendersRenames(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.3", "SubRoot", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.3.1", "SubL2", "proj-a1.3", "open")
	seedTaskWithStatus(t, s, "proj-b2", "B2", "", "open")

	cfg := plannerCfg()
	cfg.Output.Text = true
	err, stdout, _ := runMove(t, s, cfg, []string{"proj-a1.3", "--parent", "proj-b2"})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !strings.Contains(stdout, "proj-a1.3 → proj-b2.1") {
		t.Errorf("stdout missing root rename; got %q", stdout)
	}
	if !strings.Contains(stdout, "proj-a1.3.1 → proj-b2.1.1") {
		t.Errorf("stdout missing descendant rename; got %q", stdout)
	}
}

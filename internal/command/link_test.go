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

func runLink(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Link(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

func runUnlink(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Unlink(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// seedTaskTyped inserts a task with explicit type/status so the
// link tests can build sources of `type=bug` and targets of
// failed/cancelled status.
func seedTaskTyped(t *testing.T, s store.Store, id, title, status, taskType string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, type, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, title, status, taskType, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestLinkBlockedByHappyPath: default --blocked-by link via positional
// TARGET; row appears, history fires, ack carries the edge.
func TestLinkBlockedByHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Source", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "open")

	err, stdout, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	var ack struct {
		Task     string `json:"task"`
		Target   string `json:"target"`
		LinkType string `json:"link_type"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if ack.Task != "proj-a1" || ack.Target != "proj-a2" || ack.LinkType != "blocked-by" {
		t.Errorf("ack = %+v, want {proj-a1, proj-a2, blocked-by}", ack)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM dependencies WHERE task_id='proj-a1' AND target_id='proj-a2' AND link_type='blocked-by'").Scan(&n)
	if n != 1 {
		t.Errorf("dep row = %d, want 1", n)
	}
	var h int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='linked'").Scan(&h)
	if h != 1 {
		t.Errorf("history rows = %d, want 1", h)
	}
}

// TestLinkCausedByRequiresBugSource: --caused-by from a non-bug source
// fails with exit 5 (source_type_required).
func TestLinkCausedByRequiresBugSource(t *testing.T) {
	s, _ := testStore(t)
	seedTaskTyped(t, s, "proj-a1", "Source", "open", "task")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "open")

	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--caused-by", "proj-a2"})
	if err == nil {
		t.Fatal("Link: got nil, want ErrConflict")
	}
	if !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
}

// TestLinkCausedByOnBugSource: caused-by from a bug source succeeds.
func TestLinkCausedByOnBugSource(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskTyped(t, s, "proj-a1", "Source", "open", "bug")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "open")

	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--caused-by", "proj-a2"})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM dependencies WHERE task_id='proj-a1' AND target_id='proj-a2' AND link_type='caused-by'").Scan(&n)
	if n != 1 {
		t.Errorf("dep row = %d, want 1", n)
	}
}

// TestLinkDiscoveredFromOnBugSource verifies the second bug-only link
// type round-trips the same way as caused-by.
func TestLinkDiscoveredFromOnBugSource(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskTyped(t, s, "proj-a1", "Bug", "open", "bug")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "open")

	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--discovered-from", "proj-a2"})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM dependencies WHERE task_id='proj-a1' AND target_id='proj-a2' AND link_type='discovered-from'").Scan(&n)
	if n != 1 {
		t.Errorf("dep row = %d, want 1", n)
	}
}

// TestLinkRetryOfRequiresFailedTarget: retry-of with an open target
// fails (must be failed); with failed target it succeeds.
func TestLinkRetryOfRequiresFailedTarget(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Retry", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "open")

	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--retry-of", "proj-a2"})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestLinkRetryOfWithFailedTarget(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Retry", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "failed")

	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--retry-of", "proj-a2"})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM dependencies WHERE link_type='retry-of'").Scan(&n)
	if n != 1 {
		t.Errorf("retry-of row = %d, want 1", n)
	}
}

// TestLinkBlockedByCancelledRejected: blocked-by a cancelled task is
// rejected with exit 5.
func TestLinkBlockedByCancelledRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Source", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "cancelled")

	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

// TestLinkCycleDetected: A blocked-by B then linking B blocked-by A
// trips the cycle detector with exit 5.
func TestLinkCycleDetected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")

	if err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"}); err != nil {
		t.Fatalf("first Link: %v", err)
	}
	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a2", "--blocked-by", "proj-a1"})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("expected cycle ErrConflict, got %v", err)
	}
}

// TestLinkSelfBlockedByRejected: A blocked-by A rejected.
func TestLinkSelfBlockedByRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a1"})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

// TestLinkSelfBlockedByMissingReturnsNotFound pins spec §Error
// precedence: existence (exit 3) must beat cycle (exit 5). Before the
// fix, the self-reference short-circuit fired before the source
// existence SELECT, so `quest link ghost --blocked-by ghost` returned
// ErrConflict on a missing task.
func TestLinkSelfBlockedByMissingReturnsNotFound(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runLink(t, s, plannerCfg(), []string{"ghost", "--blocked-by", "ghost"})
	if err == nil || !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestLinkDuplicateNoOp: duplicate (task, target, type) emits ack but
// writes no second history row.
func TestLinkDuplicateNoOp(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")

	if err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	err, stdout, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !strings.Contains(stdout, `"target":"proj-a2"`) {
		t.Errorf("stdout missing target: %q", stdout)
	}

	var depRows int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM dependencies WHERE task_id='proj-a1' AND target_id='proj-a2' AND link_type='blocked-by'").Scan(&depRows)
	if depRows != 1 {
		t.Errorf("dep rows = %d, want 1", depRows)
	}
	var hRows int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='linked'").Scan(&hRows)
	if hRows != 1 {
		t.Errorf("linked history rows = %d, want 1", hRows)
	}
}

// TestLinkMultiTypeBetweenSamePair: caused-by + discovered-from on the
// same (task, target) pair both succeed since the uniqueness key
// includes link_type.
func TestLinkMultiTypeBetweenSamePair(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskTyped(t, s, "proj-a1", "Bug", "open", "bug")
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "open")

	if err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--caused-by", "proj-a2"}); err != nil {
		t.Fatalf("caused-by: %v", err)
	}
	if err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--discovered-from", "proj-a2"}); err != nil {
		t.Fatalf("discovered-from: %v", err)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM dependencies WHERE task_id='proj-a1' AND target_id='proj-a2'").Scan(&n)
	if n != 2 {
		t.Errorf("dep rows = %d, want 2", n)
	}
}

// TestLinkUnknownTask: source missing → exit 3.
func TestLinkUnknownTask(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a2", "Target", "", "open")
	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestLinkUnknownTarget: target missing → exit 3.
func TestLinkUnknownTarget(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Source", "", "open")
	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestLinkRejectsTwoRelationshipFlags: two flags is exit 2.
func TestLinkRejectsTwoRelationshipFlags(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")
	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2", "--caused-by", "proj-a2"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestLinkMissingTarget: no target value → exit 2.
func TestLinkMissingTarget(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestUnlinkRoundTrip: link then unlink → no row, two history rows
// (linked + unlinked).
func TestUnlinkRoundTrip(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")

	if err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"}); err != nil {
		t.Fatalf("link: %v", err)
	}
	err, stdout, _ := runUnlink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if !strings.Contains(stdout, `"link_type":"blocked-by"`) {
		t.Errorf("stdout = %q", stdout)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM dependencies WHERE task_id='proj-a1' AND target_id='proj-a2' AND link_type='blocked-by'").Scan(&n)
	if n != 0 {
		t.Errorf("deps after unlink = %d, want 0", n)
	}
	var linkedH, unlinkedH int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='linked'").Scan(&linkedH)
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='unlinked'").Scan(&unlinkedH)
	if linkedH != 1 || unlinkedH != 1 {
		t.Errorf("history (linked, unlinked) = (%d, %d), want (1, 1)", linkedH, unlinkedH)
	}
}

// TestUnlinkMissingEdgeIsNoOp: unlinking an absent edge succeeds with
// no history row.
func TestUnlinkMissingEdgeIsNoOp(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")

	err, stdout, _ := runUnlink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	var ack struct {
		Task, Target, Type string
	}
	_ = json.Unmarshal([]byte(stdout), &ack)
	if ack.Task != "proj-a1" {
		t.Errorf("ack.Task = %q, want proj-a1", ack.Task)
	}
	var h int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='unlinked'").Scan(&h)
	if h != 0 {
		t.Errorf("unlinked history rows = %d, want 0", h)
	}
}

// TestUnlinkUnknownTask: missing source → exit 3.
func TestUnlinkUnknownTask(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runUnlink(t, s, plannerCfg(), []string{"proj-a1", "--blocked-by", "proj-a2"})
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestLinkRejectsBareTargetPositional pins the spec §Linking surface:
// `quest link TASK TARGET` (no relationship flag) is exit 2. Previously
// the code accepted the second bare positional as a --blocked-by
// shorthand; the spec synopsis requires an explicit flag.
func TestLinkRejectsBareTargetPositional(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")

	err, _, _ := runLink(t, s, plannerCfg(), []string{"proj-a1", "proj-a2"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestUnlinkRejectsBareTargetPositional mirrors the link case: unlink
// also requires an explicit relationship flag.
func TestUnlinkRejectsBareTargetPositional(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	seedTaskWithStatus(t, s, "proj-a2", "B", "", "open")

	err, _, _ := runUnlink(t, s, plannerCfg(), []string{"proj-a1", "proj-a2"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// _ = sql so the import survives a future change that drops the
// only sql.NullString reference.
var _ = sql.NullString{}

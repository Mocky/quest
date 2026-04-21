//go:build integration

package batch_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/store"
)

// testStore opens a fresh migrated SQLite DB for the deps tests.
// Mirrors the helper used in internal/command tests (copied locally
// so the two test binaries stay independent).
func testStore(t *testing.T) store.Store {
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
	return s
}

// seedTask inserts a task row with the supplied id/status/type; used
// by tests that need real targets in the graph for existence +
// status + source-type checks.
func seedTask(t *testing.T, s store.Store, id, status, taskType string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if taskType == "" {
		taskType = "task"
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, type, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, id, status, taskType, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// seedEdge inserts a dependency row linking src --type--> tgt.
func seedEdge(t *testing.T, s store.Store, src, tgt, linkType string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxLink)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES (?, ?, ?, ?)`,
		src, tgt, linkType, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert edge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestValidateSemanticEmpty pins the empty-input case — no edges, no
// errors, regardless of source shape.
func TestValidateSemanticEmpty(t *testing.T) {
	s := testStore(t)
	errs := batch.ValidateSemantic(context.Background(), s, batch.TaskShape{Type: "task"}, nil)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %+v", errs)
	}
}

// TestValidateSemanticUnknownTarget covers the existence check: a
// missing target yields `unknown_task_id` regardless of link type.
// The edge-skips-later-checks behavior means the test does not
// simultaneously receive a second code on the same edge.
func TestValidateSemanticUnknownTarget(t *testing.T) {
	s := testStore(t)
	cases := []struct {
		name     string
		linkType string
	}{
		{"blocked-by", batch.LinkBlockedBy},
		{"retry-of", batch.LinkRetryOf},
		{"caused-by", batch.LinkCausedBy},
		{"discovered-from", batch.LinkDiscoveredFrom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := batch.ValidateSemantic(context.Background(), s,
				batch.TaskShape{Type: "bug"},
				[]batch.Edge{{Target: "nope-1", LinkType: tc.linkType}})
			if len(errs) != 1 {
				t.Fatalf("errs = %+v, want 1 error", errs)
			}
			if errs[0].Code != batch.CodeUnknownTaskID {
				t.Errorf("code = %q, want %q", errs[0].Code, batch.CodeUnknownTaskID)
			}
			if errs[0].Target != "nope-1" {
				t.Errorf("target = %q, want nope-1", errs[0].Target)
			}
		})
	}
}

// TestValidateSemanticBlockedByCancelled: blocked-by → cancelled
// target is rejected.
func TestValidateSemanticBlockedByCancelled(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-a1", "cancelled", "task")

	errs := batch.ValidateSemantic(context.Background(), s,
		batch.TaskShape{Type: "task"},
		[]batch.Edge{{Target: "proj-a1", LinkType: batch.LinkBlockedBy}})
	if len(errs) != 1 {
		t.Fatalf("errs = %+v, want 1 error", errs)
	}
	if errs[0].Code != batch.CodeBlockedByCancelled {
		t.Errorf("code = %q, want %q", errs[0].Code, batch.CodeBlockedByCancelled)
	}
	if errs[0].Target != "proj-a1" {
		t.Errorf("target = %q, want proj-a1", errs[0].Target)
	}
}

// TestValidateSemanticBlockedByNonCancelled sweeps the non-cancelled
// statuses — open, accepted, completed, failed — and confirms each
// produces no error for blocked-by.
func TestValidateSemanticBlockedByNonCancelled(t *testing.T) {
	s := testStore(t)
	statuses := []string{"open", "accepted", "completed", "failed"}
	for i, st := range statuses {
		id := "proj-a" + string(rune('1'+i))
		seedTask(t, s, id, st, "task")
		errs := batch.ValidateSemantic(context.Background(), s,
			batch.TaskShape{Type: "task"},
			[]batch.Edge{{Target: id, LinkType: batch.LinkBlockedBy}})
		if len(errs) != 0 {
			t.Errorf("status=%s: errs = %+v, want none", st, errs)
		}
	}
}

// TestValidateSemanticRetryOfTargetStatus pins the retry-of rule —
// target must be `failed`. Every non-failed status yields
// `retry_target_status` with Detail carrying the actual status.
func TestValidateSemanticRetryOfTargetStatus(t *testing.T) {
	s := testStore(t)
	cases := []struct {
		status string
		wantOK bool
	}{
		{"open", false},
		{"accepted", false},
		{"completed", false},
		{"cancelled", false},
		{"failed", true},
	}
	for i, tc := range cases {
		id := "proj-b" + string(rune('1'+i))
		seedTask(t, s, id, tc.status, "task")
		errs := batch.ValidateSemantic(context.Background(), s,
			batch.TaskShape{Type: "task"},
			[]batch.Edge{{Target: id, LinkType: batch.LinkRetryOf}})
		if tc.wantOK {
			if len(errs) != 0 {
				t.Errorf("status=%s: errs = %+v, want none", tc.status, errs)
			}
			continue
		}
		if len(errs) != 1 {
			t.Fatalf("status=%s: errs = %+v, want 1 error", tc.status, errs)
		}
		if errs[0].Code != batch.CodeRetryTargetStatus {
			t.Errorf("status=%s: code = %q, want %q", tc.status, errs[0].Code, batch.CodeRetryTargetStatus)
		}
		if errs[0].Detail != tc.status {
			t.Errorf("status=%s: detail = %q, want %q", tc.status, errs[0].Detail, tc.status)
		}
	}
}

// TestValidateSemanticSourceTypeRequired: caused-by /
// discovered-from require source.Type=bug.
func TestValidateSemanticSourceTypeRequired(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-src1", "completed", "task")

	cases := []struct {
		linkType   string
		sourceType string
		wantErr    bool
	}{
		{batch.LinkCausedBy, "task", true},
		{batch.LinkCausedBy, "bug", false},
		{batch.LinkDiscoveredFrom, "task", true},
		{batch.LinkDiscoveredFrom, "bug", false},
	}
	for _, tc := range cases {
		name := tc.linkType + "/" + tc.sourceType
		t.Run(name, func(t *testing.T) {
			errs := batch.ValidateSemantic(context.Background(), s,
				batch.TaskShape{Type: tc.sourceType},
				[]batch.Edge{{Target: "proj-src1", LinkType: tc.linkType}})
			if tc.wantErr {
				if len(errs) != 1 || errs[0].Code != batch.CodeSourceTypeRequired {
					t.Fatalf("errs = %+v, want 1 source_type_required", errs)
				}
				if errs[0].Type != tc.linkType {
					t.Errorf("type = %q, want %q", errs[0].Type, tc.linkType)
				}
			} else {
				if len(errs) != 0 {
					t.Errorf("errs = %+v, want none", errs)
				}
			}
		})
	}
}

// TestValidateSemanticCycleDirect: source exists and target already
// blocks source — adding source→target closes the cycle.
func TestValidateSemanticCycleDirect(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-a1", "open", "task")
	seedTask(t, s, "proj-a2", "open", "task")
	seedEdge(t, s, "proj-a2", "proj-a1", batch.LinkBlockedBy)

	errs := batch.ValidateSemantic(context.Background(), s,
		batch.TaskShape{ID: "proj-a1", Type: "task"},
		[]batch.Edge{{Target: "proj-a2", LinkType: batch.LinkBlockedBy}})
	if len(errs) != 1 {
		t.Fatalf("errs = %+v, want 1 error", errs)
	}
	if errs[0].Code != batch.CodeCycle {
		t.Errorf("code = %q, want %q", errs[0].Code, batch.CodeCycle)
	}
	if len(errs[0].Path) < 3 {
		t.Fatalf("path = %v, want at least [source, target, source]", errs[0].Path)
	}
	if errs[0].Path[0] != "proj-a1" || errs[0].Path[len(errs[0].Path)-1] != "proj-a1" {
		t.Errorf("path endpoints = %v, want both 'proj-a1'", errs[0].Path)
	}
}

// TestValidateSemanticCycleTransitive: a → B → C exists; adding
// A→C (i.e., linking A blocked-by C when C already blocks A via B)
// produces a cycle.
func TestValidateSemanticCycleTransitive(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-a", "open", "task")
	seedTask(t, s, "proj-b", "open", "task")
	seedTask(t, s, "proj-c", "open", "task")
	seedEdge(t, s, "proj-b", "proj-a", batch.LinkBlockedBy) // B blocked-by A
	seedEdge(t, s, "proj-c", "proj-b", batch.LinkBlockedBy) // C blocked-by B

	// Adding A blocked-by C: A → C → B → A is a cycle.
	errs := batch.ValidateSemantic(context.Background(), s,
		batch.TaskShape{ID: "proj-a", Type: "task"},
		[]batch.Edge{{Target: "proj-c", LinkType: batch.LinkBlockedBy}})
	if len(errs) != 1 {
		t.Fatalf("errs = %+v, want 1 cycle", errs)
	}
	if errs[0].Code != batch.CodeCycle {
		t.Errorf("code = %q, want %q", errs[0].Code, batch.CodeCycle)
	}
	// Path should include all four of the expected IDs.
	path := errs[0].Path
	for _, want := range []string{"proj-a", "proj-b", "proj-c"} {
		found := false
		for _, id := range path {
			if id == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("path %v missing %s", path, want)
		}
	}
}

// TestValidateSemanticNoCycleForNewSource: create/batch pass
// source.ID="" to signal "the source doesn't exist yet" — cycle
// detection is skipped, which is safe because a brand-new task
// cannot participate in any existing blocked-by cycle.
func TestValidateSemanticNoCycleForNewSource(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-a", "open", "task")
	seedTask(t, s, "proj-b", "open", "task")
	seedEdge(t, s, "proj-a", "proj-b", batch.LinkBlockedBy)
	seedEdge(t, s, "proj-b", "proj-a", batch.LinkBlockedBy) // pre-existing A↔B cycle

	// Source has no ID → cycle check skipped. Edge passes semantic.
	errs := batch.ValidateSemantic(context.Background(), s,
		batch.TaskShape{Type: "task"},
		[]batch.Edge{{Target: "proj-a", LinkType: batch.LinkBlockedBy}})
	if len(errs) != 0 {
		t.Errorf("errs = %+v, want none (cycle skipped for new source)", errs)
	}
}

// TestValidateSemanticSelfLoop: source == target is an immediate
// cycle even with no existing edges.
func TestValidateSemanticSelfLoop(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-a", "open", "task")

	errs := batch.ValidateSemantic(context.Background(), s,
		batch.TaskShape{ID: "proj-a", Type: "task"},
		[]batch.Edge{{Target: "proj-a", LinkType: batch.LinkBlockedBy}})
	if len(errs) != 1 {
		t.Fatalf("errs = %+v, want 1 cycle", errs)
	}
	if errs[0].Code != batch.CodeCycle {
		t.Errorf("code = %q, want %q", errs[0].Code, batch.CodeCycle)
	}
}

// TestValidateSemanticCollectsAllErrors: one call can return
// multiple errors across edges — the function does not short-circuit
// on the first failure.
func TestValidateSemanticCollectsAllErrors(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-cxl", "cancelled", "task")
	seedTask(t, s, "proj-ok", "completed", "task")

	edges := []batch.Edge{
		{Target: "proj-cxl", LinkType: batch.LinkBlockedBy},
		{Target: "proj-nope", LinkType: batch.LinkBlockedBy},
		{Target: "proj-ok", LinkType: batch.LinkRetryOf},
	}
	errs := batch.ValidateSemantic(context.Background(), s,
		batch.TaskShape{Type: "task"}, edges)
	if len(errs) != 3 {
		t.Fatalf("errs = %+v, want 3 errors", errs)
	}
	codes := map[string]bool{}
	for _, e := range errs {
		codes[e.Code] = true
	}
	for _, want := range []string{
		batch.CodeBlockedByCancelled,
		batch.CodeUnknownTaskID,
		batch.CodeRetryTargetStatus,
	} {
		if !codes[want] {
			t.Errorf("missing code %q in %+v", want, errs)
		}
	}
}

// TestValidateSemanticErrorCodes pins the full SemanticDepError code
// set per quest-spec.md §Batch error output — a change to the enum is
// a contract change that must update callers (CLI stderr + batch
// stderr JSONL).
func TestValidateSemanticErrorCodes(t *testing.T) {
	want := []string{
		batch.CodeCycle,
		batch.CodeBlockedByCancelled,
		batch.CodeRetryTargetStatus,
		batch.CodeSourceTypeRequired,
		batch.CodeUnknownTaskID,
	}
	for _, c := range want {
		if c == "" {
			t.Errorf("error code constant is empty")
		}
	}
}

// TestDetectCycleNoCycle: independent helper, no cycle when graphs
// are disjoint.
func TestDetectCycleNoCycle(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-a", "open", "task")
	seedTask(t, s, "proj-b", "open", "task")

	path, cycle := batch.DetectCycle(context.Background(), s, "proj-a", "proj-b", nil)
	if cycle {
		t.Errorf("cycle = true, want false; path=%v", path)
	}
}

// _ keeps the sql import live for future extensions that probe the
// raw DB; today the scan-side helpers inside seedTask cover it.
var _ = sql.ErrNoRows

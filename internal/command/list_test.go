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

func runList(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.List(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// seedListTask inserts a task with explicit role/tier columns so the
// list tests can exercise every enum filter. Nullable columns are
// persisted as NULL when the corresponding arg is "". The `taskType`
// parameter is retained as a positional no-op so existing call sites
// keep compiling after migration 006 dropped the type column; `bug` is
// now a tag and seeded via seedTag at the call site if needed.
func seedListTask(t *testing.T, s store.Store, id, title, parent, status, role, tier, taskType string) {
	t.Helper()
	_ = taskType
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	var parentArg, roleArg, tierArg any = sql.NullString{}, sql.NullString{}, sql.NullString{}
	if parent != "" {
		parentArg = parent
	}
	if role != "" {
		roleArg = role
	}
	if tier != "" {
		tierArg = tier
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, role, tier, parent, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, title, status, roleArg, tierArg, parentArg, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// parseListArray unmarshals stdout as an array of keyed objects so
// tests can assert over columns per spec row shape.
func parseListArray(t *testing.T, stdout string) []map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout not JSON array: %v; raw=%q", err, stdout)
	}
	return rows
}

// TestListEmpty: no tasks → [] (exit 0).
func TestListEmpty(t *testing.T) {
	s, _ := testStore(t)
	err, stdout, _ := runList(t, s, plannerCfg(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want []", stdout)
	}
}

// TestListDefaultColumns pins id/status/blocked-by/title as the
// default column set; title is last, blocked-by is the array.
func TestListDefaultColumns(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")

	err, stdout, _ := runList(t, s, plannerCfg(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	want := []string{"id", "status", "blocked-by", "title"}
	row := rows[0]
	if len(row) != len(want) {
		t.Errorf("row keys = %v, want %v", keysOf(row), want)
	}
	for _, k := range want {
		if _, ok := row[k]; !ok {
			t.Errorf("missing key %q in row %v", k, row)
		}
	}
	if string(row["blocked-by"]) != "[]" {
		t.Errorf("blocked-by = %s, want []", row["blocked-by"])
	}
	if string(row["id"]) != `"proj-a1"` {
		t.Errorf("id = %s, want proj-a1", row["id"])
	}
}

// TestListDefaultStatusExcludesCancelled pins the cross-cutting rule:
// a `cancelled` task is omitted from the default listing, but
// `--status cancelled` honors the explicit filter.
func TestListDefaultStatusExcludesCancelled(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Dropped", "", "cancelled", "", "", "")

	err, stdout, _ := runList(t, s, plannerCfg(), nil)
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a1"` {
		t.Errorf("default rows = %+v, want just proj-a1", rows)
	}

	// Explicit cancelled in the filter returns the cancelled row.
	err, stdout, _ = runList(t, s, plannerCfg(), []string{"--status", "cancelled"})
	if err != nil {
		t.Fatalf("List --status cancelled: %v", err)
	}
	rows = parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a2"` {
		t.Errorf("rows = %+v, want just proj-a2", rows)
	}
}

// TestListStatusOR: comma-separated --status matches any of them.
func TestListStatusOR(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "failed", "", "", "")
	seedListTask(t, s, "proj-a3", "Gamma", "", "completed", "", "", "")

	err, stdout, _ := runList(t, s, plannerCfg(), []string{"--status", "open,failed"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}

// TestListStatusRepeatableUnion: repeated --status flags union.
func TestListStatusRepeatableUnion(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "failed", "", "", "")
	seedListTask(t, s, "proj-a3", "Gamma", "", "completed", "", "", "")

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--status", "open", "--status", "failed"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}

// TestListRoleTierFilters: each enum filter narrows independently,
// composed AND across flags.
func TestListRoleTierFilters(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "coder", "T2", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "open", "coder", "T3", "")
	seedListTask(t, s, "proj-a3", "Gamma", "", "open", "reviewer", "T2", "")

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--role", "coder", "--tier", "T2,T3"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}

// TestListParentFilter: --parent scopes to children of the parent(s).
func TestListParentFilter(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Parent", "", "open", "", "", "")
	seedListTask(t, s, "proj-a1.1", "Child-1", "proj-a1", "open", "", "", "")
	seedListTask(t, s, "proj-a1.2", "Child-2", "proj-a1", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Sibling", "", "open", "", "", "")

	err, stdout, _ := runList(t, s, plannerCfg(), []string{"--parent", "proj-a1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}

// TestListTagAND: comma within a single --tag flag is AND
// (intersection). A row matches only if it carries every listed tag.
func TestListTagAND(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "open", "", "", "")
	seedListTask(t, s, "proj-a3", "Gamma", "", "open", "", "", "")
	seedTag(t, s, "proj-a1", "go")
	seedTag(t, s, "proj-a1", "auth")
	seedTag(t, s, "proj-a2", "go")
	seedTag(t, s, "proj-a3", "auth")

	err, stdout, _ := runList(t, s, plannerCfg(), []string{"--tag", "go,auth"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a1"` {
		t.Errorf("rows = %+v, want just proj-a1", rows)
	}
}

// TestListTagRepeatOR: repeating --tag flags is OR -- each occurrence
// is a separate AND-arm; rows match if any arm matches. With single-
// element arms this is the union of single-tag matches.
func TestListTagRepeatOR(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "open", "", "", "")
	seedListTask(t, s, "proj-a3", "Gamma", "", "open", "", "", "")
	seedListTask(t, s, "proj-a4", "Delta", "", "open", "", "", "")
	seedTag(t, s, "proj-a1", "go")
	seedTag(t, s, "proj-a2", "auth")
	seedTag(t, s, "proj-a3", "ui")
	// proj-a4 has no tags

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--tag", "go", "--tag", "auth"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2; got %+v", len(rows), rows)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[string(r["id"])] = true
	}
	for _, want := range []string{`"proj-a1"`, `"proj-a2"`} {
		if !seen[want] {
			t.Errorf("missing id %s; rows = %+v", want, rows)
		}
	}
}

// TestListTagDNF: `--tag a,b --tag c,d` expresses
// `(a AND b) OR (c AND d)` -- the canonical DNF case the new
// semantics enable.
func TestListTagDNF(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "open", "", "", "")
	seedListTask(t, s, "proj-a3", "Gamma", "", "open", "", "", "")
	seedListTask(t, s, "proj-a4", "Delta", "", "open", "", "", "")
	// proj-a1 matches arm 1 (go AND auth)
	seedTag(t, s, "proj-a1", "go")
	seedTag(t, s, "proj-a1", "auth")
	// proj-a2 has only one of (go, auth) -- does not match arm 1
	seedTag(t, s, "proj-a2", "go")
	// proj-a3 matches arm 2 (bug AND frontend)
	seedTag(t, s, "proj-a3", "bug")
	seedTag(t, s, "proj-a3", "frontend")
	// proj-a4 has only one of (bug, frontend) -- does not match arm 2
	seedTag(t, s, "proj-a4", "bug")

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--tag", "go,auth", "--tag", "bug,frontend"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2; got %+v", len(rows), rows)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[string(r["id"])] = true
	}
	for _, want := range []string{`"proj-a1"`, `"proj-a3"`} {
		if !seen[want] {
			t.Errorf("missing id %s; rows = %+v", want, rows)
		}
	}
}

// TestListTagEmptyRejected: --tag "" is exit 2 (usage error).
func TestListTagEmptyRejected(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runList(t, s, plannerCfg(), []string{"--tag", ""})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestListTagInvalidCharRejected: tag character-class violation
// (punctuation, underscore, etc.) returns exit 2 -- the same
// validation `quest tag` and `quest create --tag` apply, wired
// through the list parser.
func TestListTagInvalidCharRejected(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runList(t, s, plannerCfg(), []string{"--tag", "_go"})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestListBlockedByAND: comma within a single --blocked-by is AND --
// a task matches only if it holds blocked-by edges to every listed
// target.
func TestListBlockedByAND(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Upstream-1", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Upstream-2", "", "open", "", "", "")
	seedListTask(t, s, "proj-b1", "Down-both", "", "open", "", "", "")
	seedListTask(t, s, "proj-b2", "Down-a1-only", "", "open", "", "", "")
	seedDep(t, s, "proj-b1", "proj-a1", "blocked-by")
	seedDep(t, s, "proj-b1", "proj-a2", "blocked-by")
	seedDep(t, s, "proj-b2", "proj-a1", "blocked-by")

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--blocked-by", "proj-a1,proj-a2"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-b1"` {
		t.Errorf("rows = %+v, want just proj-b1", rows)
	}
}

// TestListBlockedByRepeatOR: repeating --blocked-by yields OR --
// matches any task holding a blocked-by edge to any listed target.
func TestListBlockedByRepeatOR(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Upstream-1", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Upstream-2", "", "open", "", "", "")
	seedListTask(t, s, "proj-b1", "Down-a1", "", "open", "", "", "")
	seedListTask(t, s, "proj-b2", "Down-a2", "", "open", "", "", "")
	seedListTask(t, s, "proj-b3", "Down-other", "", "open", "", "", "")
	seedDep(t, s, "proj-b1", "proj-a1", "blocked-by")
	seedDep(t, s, "proj-b2", "proj-a2", "blocked-by")
	// proj-b3 has no blocked-by edges -- should not match

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--blocked-by", "proj-a1", "--blocked-by", "proj-a2"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2; got %+v", len(rows), rows)
	}
}

// TestListBlockedByDNF: `--blocked-by A,B --blocked-by C` expresses
// `(A AND B) OR C`.
func TestListBlockedByDNF(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Up-1", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Up-2", "", "open", "", "", "")
	seedListTask(t, s, "proj-a3", "Up-3", "", "open", "", "", "")
	seedListTask(t, s, "proj-b1", "Down-both-a1-a2", "", "open", "", "", "")
	seedListTask(t, s, "proj-b2", "Down-a3", "", "open", "", "", "")
	seedListTask(t, s, "proj-b3", "Down-a1-only", "", "open", "", "", "")
	seedDep(t, s, "proj-b1", "proj-a1", "blocked-by")
	seedDep(t, s, "proj-b1", "proj-a2", "blocked-by")
	seedDep(t, s, "proj-b2", "proj-a3", "blocked-by")
	seedDep(t, s, "proj-b3", "proj-a1", "blocked-by") // missing a2 -- arm 1 fails

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--blocked-by", "proj-a1,proj-a2", "--blocked-by", "proj-a3"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2; got %+v", len(rows), rows)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[string(r["id"])] = true
	}
	for _, want := range []string{`"proj-b1"`, `"proj-b2"`} {
		if !seen[want] {
			t.Errorf("missing id %s; rows = %+v", want, rows)
		}
	}
}

// TestListBlockedByEmptyRejected: --blocked-by "" is exit 2.
func TestListBlockedByEmptyRejected(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runList(t, s, plannerCfg(), []string{"--blocked-by", ""})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestListBlockedByUnknownTargetZeroRows: an unknown target ID is
// not a usage error -- it matches zero rows, mirroring how --parent
// treats unknown values.
func TestListBlockedByUnknownTargetZeroRows(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--blocked-by", "proj-nonexistent"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want []", stdout)
	}
}

// TestListBlockedByMixedWithTag: cross-filter composition is AND --
// `--blocked-by X --tag bug` returns rows matching both filters.
func TestListBlockedByMixedWithTag(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Upstream", "", "open", "", "", "")
	seedListTask(t, s, "proj-b1", "Both", "", "open", "", "", "")
	seedListTask(t, s, "proj-b2", "Tagged-only", "", "open", "", "", "")
	seedListTask(t, s, "proj-b3", "Blocked-only", "", "open", "", "", "")
	seedDep(t, s, "proj-b1", "proj-a1", "blocked-by")
	seedDep(t, s, "proj-b3", "proj-a1", "blocked-by")
	seedTag(t, s, "proj-b1", "bug")
	seedTag(t, s, "proj-b2", "bug")

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--blocked-by", "proj-a1", "--tag", "bug"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-b1"` {
		t.Errorf("rows = %+v, want just proj-b1", rows)
	}
}

// TestListMultiValuedDedupArms: identical arms across repeats fold
// to one. Order within an arm doesn't matter for dedup, so
// `--tag a,b --tag b,a` is the same single arm.
func TestListMultiValuedDedupArms(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "open", "", "", "")
	seedTag(t, s, "proj-a1", "go")
	seedTag(t, s, "proj-a1", "auth")
	seedTag(t, s, "proj-a2", "go")

	// `--tag a,b --tag b,a` collapses to one arm `(a AND b)`.
	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--tag", "go,auth", "--tag", "auth,go"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a1"` {
		t.Errorf("rows = %+v, want just proj-a1", rows)
	}
}

// TestListReadyLeafOpen: a leaf in open with no blocked-by edges is
// ready.
func TestListReadyLeafOpen(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	seedListTask(t, s, "proj-a2", "Beta", "", "accepted", "", "", "")

	err, stdout, _ := runList(t, s, plannerCfg(), []string{"--ready"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a1"` {
		t.Errorf("rows = %+v, want just proj-a1", rows)
	}
}

// TestListReadyLeafBlocked: a leaf with an incomplete blocked-by
// target is NOT ready.
func TestListReadyLeafBlocked(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Upstream", "", "accepted", "", "", "")
	seedListTask(t, s, "proj-a2", "Downstream", "", "open", "", "", "")
	seedDep(t, s, "proj-a2", "proj-a1", "blocked-by")

	err, stdout, _ := runList(t, s, plannerCfg(), []string{"--ready"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want 0 (blocked by incomplete upstream)", rows)
	}

	// Complete the upstream and the downstream becomes ready.
	if _, err := updateStatus(t, s, "proj-a1", "completed"); err != nil {
		t.Fatalf("update: %v", err)
	}
	err, stdout, _ = runList(t, s, plannerCfg(), []string{"--ready"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows = parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a2"` {
		t.Errorf("rows = %+v, want just proj-a2", rows)
	}
}

// TestListReadyParent: a parent is ready only when every child is
// terminal and no blocked-by edge is unmet.
func TestListReadyParent(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Parent", "", "open", "", "", "")
	seedListTask(t, s, "proj-a1.1", "Child-1", "proj-a1", "completed", "", "", "")
	seedListTask(t, s, "proj-a1.2", "Child-2", "proj-a1", "accepted", "", "", "")

	// Not ready: one child is not terminal.
	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--ready", "--columns", "id,status,children"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want 0 (child-2 not terminal)", rows)
	}

	if _, err := updateStatus(t, s, "proj-a1.2", "completed"); err != nil {
		t.Fatalf("update: %v", err)
	}
	err, stdout, _ = runList(t, s, plannerCfg(),
		[]string{"--ready", "--columns", "id,status,children"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows = parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a1"` {
		t.Errorf("rows = %+v, want proj-a1", rows)
	}
	// children column is a non-empty array — caller can distinguish
	// parent-ready from leaf-ready.
	if !strings.Contains(string(rows[0]["children"]), "proj-a1.1") {
		t.Errorf("children = %s, want proj-a1.1 listed", rows[0]["children"])
	}
}

// TestListUnknownStatusRejected: typo on --status returns ErrUsage
// with the "did you mean" hint.
func TestListUnknownStatusRejected(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runList(t, s, plannerCfg(), []string{"--status", "compelete"})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "completed") {
		t.Errorf("err = %q, want 'did you mean completed' hint", err.Error())
	}
}

// TestListUnknownTierRejected: typo on --tier also rejected.
func TestListUnknownTierRejected(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runList(t, s, plannerCfg(), []string{"--tier", "T9"})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestListUnknownColumnRejected: typo on --columns also rejected.
func TestListUnknownColumnRejected(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	err, _, _ := runList(t, s, plannerCfg(), []string{"--columns", "id,ttitle"})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("err = %q, want 'did you mean title'", err.Error())
	}
}

// TestListColumnsOrderPreserved: requested column order is honored in
// the JSON row keys.
func TestListColumnsOrderPreserved(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	err, stdout, _ := runList(t, s, plannerCfg(), []string{"--columns", "title,id,status"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Key-order check: since Go's encoding/json on a map sorts alphabetically,
	// we verify the raw bytes contain title before id.
	trimmed := strings.TrimSpace(stdout)
	iTitle := strings.Index(trimmed, `"title"`)
	iID := strings.Index(trimmed, `"id"`)
	iStatus := strings.Index(trimmed, `"status"`)
	if iTitle < 0 || iID < 0 || iStatus < 0 {
		t.Fatalf("stdout missing expected keys: %q", stdout)
	}
	if !(iTitle < iID && iID < iStatus) {
		t.Errorf("column order not preserved: title=%d id=%d status=%d; raw=%q", iTitle, iID, iStatus, stdout)
	}
}

// TestListRowShapeNullAndCollections: unset role/tier/parent emit
// JSON null; tags/children/blocked-by always emit arrays.
func TestListRowShapeNullAndCollections(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--columns", "id,role,tier,parent,tags,children,blocked-by"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	for _, k := range []string{"role", "tier", "parent"} {
		if string(row[k]) != "null" {
			t.Errorf("%s = %s, want null", k, row[k])
		}
	}
	for _, k := range []string{"tags", "children", "blocked-by"} {
		if string(row[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, row[k])
		}
	}
}

// TestListRejectsUnexpectedPositional: a stray positional returns
// ErrUsage.
func TestListRejectsUnexpectedPositional(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runList(t, s, plannerCfg(), []string{"proj-a1"})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestListTextFormat emits a header row and one row per task.
func TestListTextFormat(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	cfg := plannerCfg()
	cfg.Output.Text = true
	err, stdout, _ := runList(t, s, cfg, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, "STATUS") {
		t.Errorf("stdout header missing: %q", stdout)
	}
	if !strings.Contains(stdout, "proj-a1") {
		t.Errorf("stdout row missing: %q", stdout)
	}
}

// TestListTextCountFooterSingularPluralEmpty pins that the count
// footer is emitted in text mode across all three cases (0/1/N) and
// that the JSON output carries no count field -- the footer is a
// text-mode-only human affordance.
func TestListTextCountFooterSingularPluralEmpty(t *testing.T) {
	s, _ := testStore(t)
	// Empty store ⇒ text output ends with "0 tasks".
	textCfg := plannerCfg()
	textCfg.Output.Text = true
	err, stdout, _ := runList(t, s, textCfg, nil)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if !strings.HasSuffix(stdout, "\n0 tasks\n") {
		t.Errorf("empty text footer missing; got:\n%s", stdout)
	}

	// JSON on the same empty store is a bare [] with no count field.
	err, stdout, _ = runList(t, s, plannerCfg(), nil)
	if err != nil {
		t.Fatalf("List JSON empty: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("JSON empty = %q, want []", stdout)
	}

	// One task ⇒ singular footer.
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "", "", "")
	err, stdout, _ = runList(t, s, textCfg, nil)
	if err != nil {
		t.Fatalf("List one: %v", err)
	}
	if !strings.HasSuffix(stdout, "\n1 task\n") {
		t.Errorf("singular footer missing; got:\n%s", stdout)
	}
	if strings.Contains(stdout, "1 tasks") {
		t.Errorf("plural footer emitted for single row: %q", stdout)
	}

	// Three tasks ⇒ plural footer.
	seedListTask(t, s, "proj-a2", "Beta", "", "open", "", "", "")
	seedListTask(t, s, "proj-a3", "Gamma", "", "open", "", "", "")
	err, stdout, _ = runList(t, s, textCfg, nil)
	if err != nil {
		t.Fatalf("List many: %v", err)
	}
	if !strings.HasSuffix(stdout, "\n3 tasks\n") {
		t.Errorf("plural footer missing; got:\n%s", stdout)
	}

	// JSON on the non-empty store is still a bare array, no count key
	// anywhere in the payload.
	err, stdout, _ = runList(t, s, plannerCfg(), nil)
	if err != nil {
		t.Fatalf("List JSON many: %v", err)
	}
	for _, forbidden := range []string{`"count"`, `"tasks":3`, `"total"`} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("JSON leaked count field %q: %s", forbidden, stdout)
		}
	}
}

// TestListTextFormatUnboundedTitleWhenPiped pins the spec §Text-mode
// formatting no-TTY branch: when stdout is not a terminal (the buffer
// case in these integration tests), the title column is unbounded and
// a long title is emitted in full with no "..." truncation.
func TestListTextFormatUnboundedTitleWhenPiped(t *testing.T) {
	s, _ := testStore(t)
	longTitle := strings.Repeat("x", 60)
	seedListTask(t, s, "proj-t1", longTitle, "", "open", "", "", "")
	cfg := plannerCfg()
	cfg.Output.Text = true
	err, stdout, _ := runList(t, s, cfg, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(stdout, longTitle) {
		t.Errorf("full title missing: want substring %q; got %q", longTitle, stdout)
	}
	if strings.Contains(stdout, "...") {
		t.Errorf("unexpected ... truncation on piped stdout: %q", stdout)
	}
}

// updateStatus rewrites a task status via direct SQL so tests can
// re-configure the graph between calls without dragging in the full
// handler wiring.
func updateStatus(t *testing.T, s store.Store, id, status string) (sql.Result, error) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(context.Background(),
		`UPDATE tasks SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

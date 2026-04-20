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

// seedListTask inserts a task with explicit role/tier/type columns so
// the list tests can exercise every enum filter. Nullable columns are
// persisted as NULL when the corresponding arg is "".
func seedListTask(t *testing.T, s store.Store, id, title, parent, status, role, tier, taskType string) {
	t.Helper()
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
	if taskType == "" {
		taskType = "task"
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, type, role, tier, parent, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, title, status, taskType, roleArg, tierArg, parentArg, "2026-04-18T00:00:00Z"); err != nil {
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

// TestListRoleTierTypeFilters: each enum filter narrows independently,
// composed AND across flags.
func TestListRoleTierTypeFilters(t *testing.T) {
	s, _ := testStore(t)
	seedListTask(t, s, "proj-a1", "Alpha", "", "open", "coder", "T2", "task")
	seedListTask(t, s, "proj-a2", "Beta", "", "open", "coder", "T3", "task")
	seedListTask(t, s, "proj-a3", "Gamma", "", "open", "reviewer", "T2", "bug")

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--role", "coder", "--tier", "T2,T3"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}

	err, stdout, _ = runList(t, s, plannerCfg(), []string{"--type", "bug"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows = parseListArray(t, stdout)
	if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a3"` {
		t.Errorf("--type bug rows = %+v, want proj-a3", rows)
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

// TestListTagAND: multiple --tag values AND-compose (task must have
// every listed tag).
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

	// Repeated flags also AND-compose.
	err, stdout, _ = runList(t, s, plannerCfg(),
		[]string{"--tag", "go", "--tag", "auth"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows = parseListArray(t, stdout)
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

// TestListUnknownTypeRejected: typo on --type also rejected.
func TestListUnknownTypeRejected(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runList(t, s, plannerCfg(), []string{"--type", "bgu"})
	if err == nil {
		t.Fatal("got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "bug") {
		t.Errorf("err = %q, want 'did you mean bug'", err.Error())
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
	cfg.Output.Format = "text"
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

// TestListTextFormatTruncation: per spec §Text-mode formatting, cells
// that exceed the fixed column width are cut to width-3 and suffixed
// with "...". The title column width is 40, so a 60-char title lands
// at 37 chars + "...".
func TestListTextFormatTruncation(t *testing.T) {
	s, _ := testStore(t)
	longTitle := strings.Repeat("x", 60)
	seedListTask(t, s, "proj-t1", longTitle, "", "open", "", "", "")
	cfg := plannerCfg()
	cfg.Output.Format = "text"
	err, stdout, _ := runList(t, s, cfg, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := strings.Repeat("x", 37) + "..."
	if !strings.Contains(stdout, want) {
		t.Errorf("truncated title missing: want substring %q; got %q", want, stdout)
	}
	if strings.Contains(stdout, strings.Repeat("x", 38)) {
		t.Errorf("untruncated title leaked: %q", stdout)
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

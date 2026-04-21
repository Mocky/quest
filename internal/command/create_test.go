//go:build integration

package command_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// createCfg is the default planner Config used by create tests. The
// workspace IDPrefix drives ids.NewTopLevel; elevated_roles are
// role-gate inputs for the dispatcher, not directly consulted by the
// handler, but setting them keeps the cfg realistic.
func createCfg() config.Config {
	cfg := baseCfg()
	cfg.Workspace.IDPrefix = "proj"
	cfg.Workspace.ElevatedRoles = []string{"planner"}
	cfg.Agent.Role = "planner"
	cfg.Agent.Session = "sess-p1"
	return cfg
}

// runCreate invokes command.Create with args/cfg and returns the
// error, stdout, stderr triple — same shape as runShow / runAccept.
func runCreate(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	return runCreateWithStdin(t, s, cfg, args, "")
}

func runCreateWithStdin(t *testing.T, s store.Store, cfg config.Config, args []string, stdin string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Create(context.Background(), cfg, s, args, strings.NewReader(stdin), &out, &errb)
	return err, out.String(), errb.String()
}

// ackID extracts the `id` field from the create ack body; fails the
// test if stdout is not the expected shape.
func ackID(t *testing.T, stdout string) string {
	t.Helper()
	var ack struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &ack); err != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", err, stdout)
	}
	if ack.ID == "" {
		t.Fatalf("ack.id empty; raw=%q", stdout)
	}
	return ack.ID
}

// TestCreateTopLevelHappyPath: --title alone produces a task at
// proj-01, status=open, and a single `created` history entry.
func TestCreateTopLevelHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	err, stdout, _ := runCreate(t, s, createCfg(), []string{"--title", "Auth module"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)
	if id != "proj-01" {
		t.Errorf("id = %q, want proj-01", id)
	}

	var title, status string
	var parent sql.NullString
	row := queryOne(t, dbPath,
		"SELECT title, status, parent FROM tasks WHERE id='proj-01'")
	if err := row.Scan(&title, &status, &parent); err != nil {
		t.Fatalf("scan task: %v", err)
	}
	if title != "Auth module" || status != "open" {
		t.Errorf("task row = {%q, %q}, want {Auth module, open}", title, status)
	}
	if parent.Valid {
		t.Errorf("parent = %q, want SQL NULL", parent.String)
	}

	var hCount int
	hrow := queryOne(t, dbPath,
		"SELECT COUNT(*) FROM history WHERE task_id='proj-01' AND action='created'")
	if err := hrow.Scan(&hCount); err != nil {
		t.Fatalf("history: %v", err)
	}
	if hCount != 1 {
		t.Errorf("history.created count = %d, want 1", hCount)
	}
}

// TestCreateMissingTitle: the only required flag. Absent → exit 2.
func TestCreateMissingTitle(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(), []string{"--tier", "T2"})
	if err == nil {
		t.Fatalf("Create: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Errorf("err = %v, want wraps ErrUsage", err)
	}
}

// TestCreateEmptyTitle: explicit empty value rejected with exit 2.
func TestCreateEmptyTitle(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(), []string{"--title", ""})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestCreateTitleBoundary: the 128-byte cap is inclusive — a title of
// exactly 128 ASCII bytes accepts, and a 129-byte title rejects with
// exit 2 before any DB I/O. A 128-byte UTF-8 title built from 64
// 2-byte runes also accepts (pinning byte-counting semantics, not
// code-point counting); 65 of the same rune produces 130 bytes and
// rejects. Spec §Field constraints.
func TestCreateTitleBoundary(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		wantErr bool
	}{
		{"exactly 128 ASCII bytes", strings.Repeat("a", 128), false},
		{"129 ASCII bytes", strings.Repeat("a", 129), true},
		{"64 two-byte runes = 128 bytes", strings.Repeat("é", 64), false},
		{"65 two-byte runes = 130 bytes", strings.Repeat("é", 65), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			err, _, errout := runCreate(t, s, createCfg(), []string{"--title", tc.title})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got nil, want ErrUsage for %d-byte title", len(tc.title))
				}
				if !stderrors.Is(err, errors.ErrUsage) {
					t.Fatalf("err = %v, want wraps ErrUsage", err)
				}
				if !strings.Contains(err.Error(), "--title") {
					t.Errorf("err = %q, want mentions --title", err.Error())
				}
				if !strings.Contains(err.Error(), "128") {
					t.Errorf("err = %q, want mentions 128 (byte limit)", err.Error())
				}
				// The observed byte count belongs in the error too so
				// agents can tell how much they overshot without
				// recomputing len(s).
				if !strings.Contains(err.Error(), "observed") {
					t.Errorf("err = %q, want mentions observed byte size", err.Error())
				}
				_ = errout
			} else {
				if err != nil {
					t.Fatalf("Create: %v (title was %d bytes)", err, len(tc.title))
				}
			}
		})
	}
}

// TestCreateInvalidTier: --tier must be one of T0..T6. Prior to the
// shared ValidateTier helper the CLI accepted any string and deferred
// detection to an agent noticing the bogus value in `quest show`.
func TestCreateInvalidTier(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "Bad", "--tier", "T9"})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestCreateRejectsRepeatedSingleDepFlag: --caused-by, --discovered-from,
// --retry-of are single-value; a second pass rejects as usage error.
func TestCreateRejectsRepeatedSingleDepFlag(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "Bug", "--caused-by", "proj-a1", "--caused-by", "proj-a2"})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestCreateRejectsBadTag: tag with forbidden chars → exit 2.
func TestCreateRejectsBadTag(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "Tagged", "--tag", "bad_underscore"})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestCreateStoresTagsAndDeps: --tag and dep flags write rows to
// tags and dependencies tables. Tag list is lowercased, deduped, and
// sorted on the table side (alphabetical) only at read time; here
// we just assert all tags are present.
func TestCreateStoresTagsAndDeps(t *testing.T) {
	s, dbPath := testStore(t)
	// Seed a target task for the blocked-by link.
	seedTaskWithStatus(t, s, "proj-upstream", "Upstream", "", "open")

	cfg := createCfg()
	err, stdout, _ := runCreate(t, s, cfg, []string{
		"--title", "Downstream", "--tag", "Go,auth,AUTH",
		"--blocked-by", "proj-upstream",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)

	var tagCount int
	if err := queryOne(t, dbPath,
		"SELECT COUNT(*) FROM tags WHERE task_id=?").Scan(&tagCount); err == nil {
		_ = tagCount
	}
	// Simple lookup helpers: the full row list is small and the
	// query-arg convenience is limited with the sibling *sql.DB so
	// we just count with literal IDs.
	rows := queryRows(t, dbPath,
		"SELECT tag FROM tags WHERE task_id='"+id+"' ORDER BY tag")
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan tag: %v", err)
		}
		tags = append(tags, tag)
	}
	rows.Close()
	if got, want := strings.Join(tags, ","), "auth,go"; got != want {
		t.Errorf("tags = %q, want %q (lowercase + dedupe)", got, want)
	}

	var depCount int
	if err := queryOne(t, dbPath,
		"SELECT COUNT(*) FROM dependencies WHERE task_id='"+id+"' AND target_id='proj-upstream' AND link_type='blocked-by'").Scan(&depCount); err != nil {
		t.Fatalf("scan dep: %v", err)
	}
	if depCount != 1 {
		t.Errorf("dep count = %d, want 1", depCount)
	}
}

// TestCreateSubTaskUnderOpenParent: --parent resolves; id is
// parent-scoped; parent status check passes.
func TestCreateSubTaskUnderOpenParent(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Parent", "", "open")

	err, stdout, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "Child", "--parent", "proj-a1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)
	if id != "proj-a1.1" {
		t.Errorf("id = %q, want proj-a1.1", id)
	}
	var parent string
	if err := queryOne(t, dbPath, "SELECT parent FROM tasks WHERE id='"+id+"'").Scan(&parent); err != nil {
		t.Fatalf("scan parent: %v", err)
	}
	if parent != "proj-a1" {
		t.Errorf("parent = %q, want proj-a1", parent)
	}
}

// TestCreateUnknownParentExit3: existence check fires before status
// / depth checks per spec §Error precedence.
func TestCreateUnknownParentExit3(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "Child", "--parent", "proj-nope"})
	if err == nil || !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestCreateParentNotOpenExit5: accepted / completed / failed /
// cancelled parent rejects.
func TestCreateParentNotOpenExit5(t *testing.T) {
	statuses := []string{"accepted", "completed", "failed", "cancelled"}
	for _, st := range statuses {
		t.Run(st, func(t *testing.T) {
			s, _ := testStore(t)
			seedTaskWithStatus(t, s, "proj-a1", "Parent", "", st)
			err, _, _ := runCreate(t, s, createCfg(),
				[]string{"--title", "Child", "--parent", "proj-a1"})
			if err == nil || !stderrors.Is(err, errors.ErrConflict) {
				t.Fatalf("status=%s: err = %v, want wraps ErrConflict", st, err)
			}
		})
	}
}

// TestCreateDepthExceededExit5: creating under a depth-3 parent
// would produce a depth-4 ID; rejected with ErrConflict.
func TestCreateDepthExceededExit5(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "L1", "", "open")
	seedTaskWithStatus(t, s, "proj-a1.1", "L2", "proj-a1", "open")
	seedTaskWithStatus(t, s, "proj-a1.1.1", "L3", "proj-a1.1", "open")

	err, _, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "L4", "--parent", "proj-a1.1.1"})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
}

// TestCreateBlockedByCancelledTargetExit5: dependency rule fires
// after parent check — edge validator integration.
func TestCreateBlockedByCancelledTargetExit5(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-cxl", "Cancelled upstream", "", "cancelled")

	err, _, _ := runCreate(t, s, createCfg(), []string{
		"--title", "Child", "--blocked-by", "proj-cxl",
	})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
}

// TestCreateCausedBySucceeds: caused-by works on any source; no
// type gate exists after migration 006.
func TestCreateCausedBySucceeds(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Upstream", "", "completed")

	err, stdout, _ := runCreate(t, s, createCfg(), []string{
		"--title", "Regression", "--caused-by", "proj-a1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)

	var c int
	if err := queryOne(t, dbPath,
		"SELECT COUNT(*) FROM dependencies WHERE task_id='"+id+"' AND target_id='proj-a1' AND link_type='caused-by'").Scan(&c); err != nil {
		t.Fatalf("scan dep: %v", err)
	}
	if c != 1 {
		t.Errorf("caused-by count = %d, want 1", c)
	}
}

// TestCreateRetryOfRequiresFailedTarget: retry-of against any
// non-failed status rejects.
func TestCreateRetryOfRequiresFailedTarget(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "Upstream", "", "completed")

	err, _, _ := runCreate(t, s, createCfg(), []string{
		"--title", "Retry", "--retry-of", "proj-a1",
	})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
}

// TestCreateUnknownDepTargetExit5: an unresolved target produces a
// conflict (validator emits unknown_task_id → ErrConflict on
// create).
func TestCreateUnknownDepTargetExit5(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(), []string{
		"--title", "Dangling", "--blocked-by", "proj-nope",
	})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
}

// TestCreateRollsBackOnDepValidation: after a dep validation
// failure, no task row or history row lands.
func TestCreateRollsBackOnDepValidation(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-cxl", "Cancelled", "", "cancelled")

	_, _, _ = runCreate(t, s, createCfg(), []string{
		"--title", "Blocked", "--blocked-by", "proj-cxl",
	})
	var c int
	if err := queryOne(t, dbPath,
		"SELECT COUNT(*) FROM tasks WHERE id='proj-01'").Scan(&c); err != nil {
		t.Fatalf("scan tasks: %v", err)
	}
	if c != 0 {
		t.Errorf("tasks count after rollback = %d, want 0", c)
	}
}

// TestCreateDescriptionViaFile: @file resolution expands the file
// content into the description column.
func TestCreateDescriptionViaFile(t *testing.T) {
	s, dbPath := testStore(t)
	dir := t.TempDir()
	descPath := dir + "/desc.md"
	if err := writeFile(t, descPath, "# Full description\nBody here."); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	err, stdout, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "From file", "--description", "@" + descPath})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)
	var desc string
	if err := queryOne(t, dbPath, "SELECT description FROM tasks WHERE id='"+id+"'").Scan(&desc); err != nil {
		t.Fatalf("scan description: %v", err)
	}
	if !strings.Contains(desc, "Full description") {
		t.Errorf("description = %q, want content from @file", desc)
	}
}

// TestCreateStdinResolvesOnce: `@-` works once. A second `@-` flag
// produces a single-use usage error per cross-cutting §@file input.
func TestCreateStdinResolvesOnce(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreateWithStdin(t, s, createCfg(),
		[]string{"--title", "Stdin", "--description", "@-", "--context", "@-"},
		"body content")
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage (single-@- rule)", err)
	}
}

// TestCreateMetaStoresCanonicalJSON: --meta entries land in the
// metadata column as canonical JSON with sorted keys.
func TestCreateMetaStoresCanonicalJSON(t *testing.T) {
	s, dbPath := testStore(t)
	err, stdout, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "With meta", "--meta", "z=last", "--meta", "a=first"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)
	var metadata string
	if err := queryOne(t, dbPath, "SELECT metadata FROM tasks WHERE id='"+id+"'").Scan(&metadata); err != nil {
		t.Fatalf("scan metadata: %v", err)
	}
	if metadata != `{"a":"first","z":"last"}` {
		t.Errorf("metadata = %q, want canonical sorted JSON", metadata)
	}
}

// TestCreateHistoryPayloadShape asserts that non-default fields land
// in the `created` history payload and defaults are omitted.
func TestCreateHistoryPayloadShape(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-par", "Parent", "", "open")

	err, stdout, _ := runCreate(t, s, createCfg(), []string{
		"--title", "Child",
		"--parent", "proj-par",
		"--tier", "T3",
		"--role", "coder",
		"--tag", "go",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)

	var payload string
	if err := queryOne(t, dbPath,
		"SELECT payload FROM history WHERE task_id='"+id+"' AND action='created'").Scan(&payload); err != nil {
		t.Fatalf("scan payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("unmarshal payload: %v; raw=%q", err, payload)
	}
	for _, key := range []string{"tier", "role", "parent", "tags"} {
		if _, ok := m[key]; !ok {
			t.Errorf("payload missing %q; got %v", key, m)
		}
	}
}

// TestCreateNullableColumnsPersistAsNULL: --role / --tier /
// --acceptance-criteria are nullable. When absent, the columns are
// SQL NULL rather than empty string.
func TestCreateNullableColumnsPersistAsNULL(t *testing.T) {
	s, dbPath := testStore(t)
	err, stdout, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "Min"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)

	var role, tier, accept sql.NullString
	row := queryOne(t, dbPath,
		"SELECT role, tier, acceptance_criteria FROM tasks WHERE id='"+id+"'")
	if err := row.Scan(&role, &tier, &accept); err != nil {
		t.Fatalf("scan: %v", err)
	}
	for name, got := range map[string]sql.NullString{
		"role":                role,
		"tier":                tier,
		"acceptance_criteria": accept,
	} {
		if got.Valid {
			t.Errorf("%s = %q, want SQL NULL", name, got.String)
		}
	}
}

// TestCreateIDCountersMonotonic: back-to-back creates produce
// proj-01, proj-02, ... without collision. Exercises the
// tx-scoped INSERT ... ON CONFLICT ... RETURNING counter path.
func TestCreateIDCountersMonotonic(t *testing.T) {
	s, _ := testStore(t)
	want := []string{"proj-01", "proj-02", "proj-03"}
	for _, w := range want {
		_, stdout, _ := runCreate(t, s, createCfg(),
			[]string{"--title", w})
		if got := ackID(t, stdout); got != w {
			t.Errorf("id = %q, want %q", got, w)
		}
	}
}

// writeFile writes body to path with 0o644; used by @file tests.
func writeFile(t *testing.T, path, body string) error {
	t.Helper()
	return os.WriteFile(path, []byte(body), 0o644)
}

// queryRows wraps the sibling *sql.DB open with a Query call for
// multi-row results. Parallel to queryOne in accept_test.go.
func queryRows(t *testing.T, dbPath, q string) *sql.Rows {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return rows
}

//go:build integration

package command_test

import (
	"bytes"
	"context"
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

// runBatch invokes command.Batch with the supplied arguments and
// returns the error plus stdout/stderr buffers for assertions.
func runBatch(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Batch(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// writeBatchFile persists a JSONL body under t.TempDir() and
// returns the path the handler can read.
func writeBatchFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "batch.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestBatchHappyPath: a three-line batch produces three `ref/id`
// JSONL pairs on stdout, zero errors on stderr, and creates all
// three rows in the store.
func TestBatchHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	path := writeBatchFile(t,
		`{"ref":"epic","title":"Epic"}`+"\n"+
			`{"ref":"a","title":"A","parent":"epic"}`+"\n"+
			`{"ref":"b","title":"B","parent":"epic","dependencies":[{"ref":"a","type":"blocked-by"}]}`+"\n")

	err, stdout, stderr := runBatch(t, s, createCfg(), []string{path})
	if err != nil {
		t.Fatalf("Batch: %v (stderr=%q)", err, stderr)
	}
	// Three JSONL pairs on stdout.
	var pairs []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var p map[string]string
		if jerr := json.Unmarshal([]byte(line), &p); jerr != nil {
			t.Fatalf("stdout line %q not JSON: %v", line, jerr)
		}
		pairs = append(pairs, p)
	}
	if len(pairs) != 3 {
		t.Fatalf("stdout pairs = %d, want 3; raw=%q", len(pairs), stdout)
	}
	wantRefs := []string{"epic", "a", "b"}
	for i, p := range pairs {
		if p["ref"] != wantRefs[i] {
			t.Errorf("pair[%d].ref = %q, want %q", i, p["ref"], wantRefs[i])
		}
	}
	// Three task rows exist.
	var c int
	if err := queryOne(t, dbPath, "SELECT COUNT(*) FROM tasks").Scan(&c); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if c != 3 {
		t.Errorf("tasks = %d, want 3", c)
	}
}

// TestBatchAtomicFailureNoCreation: any validation error in
// atomic mode blocks the entire batch. Exit 2, no tasks created.
func TestBatchAtomicFailureNoCreation(t *testing.T) {
	s, dbPath := testStore(t)
	path := writeBatchFile(t,
		`{"ref":"a","title":"A"}`+"\n"+
			`{"ref":"b"}`+"\n") // missing title

	err, stdout, stderr := runBatch(t, s, createCfg(), []string{path})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty (atomic mode)", stdout)
	}
	if !strings.Contains(stderr, "missing_field") {
		t.Errorf("stderr missing missing_field; got %q", stderr)
	}
	var c int
	if err := queryOne(t, dbPath, "SELECT COUNT(*) FROM tasks").Scan(&c); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if c != 0 {
		t.Errorf("tasks = %d, want 0 (atomic rollback)", c)
	}
}

// TestBatchPartialOkPartialSuccess: --partial-ok creates the valid
// subset and still exits 2 because errors were reported.
func TestBatchPartialOkPartialSuccess(t *testing.T) {
	s, dbPath := testStore(t)
	path := writeBatchFile(t,
		`{"ref":"good","title":"Good"}`+"\n"+
			`{"ref":"bad"}`+"\n"+
			`{"ref":"good2","title":"Good2"}`+"\n")

	err, stdout, stderr := runBatch(t, s, createCfg(), []string{path, "--partial-ok"})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage (exit 2 even on partial success)", err)
	}
	// Two pairs should land on stdout; 'bad' is skipped.
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout lines = %d, want 2; raw=%q", len(lines), stdout)
	}
	if !strings.Contains(stderr, "missing_field") {
		t.Errorf("stderr missing missing_field; got %q", stderr)
	}
	var c int
	if err := queryOne(t, dbPath, "SELECT COUNT(*) FROM tasks").Scan(&c); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if c != 2 {
		t.Errorf("tasks = %d, want 2 (partial-ok)", c)
	}
}

// TestBatchEmptyFileReportsError: an empty file produces the
// empty_file error and exits 2 with no commit.
func TestBatchEmptyFileReportsError(t *testing.T) {
	s, _ := testStore(t)
	path := writeBatchFile(t, "")
	err, stdout, stderr := runBatch(t, s, createCfg(), []string{path})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(stderr, "empty_file") {
		t.Errorf("stderr missing empty_file; got %q", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

// TestBatchMissingFileExit2: nonexistent FILE path is a usage
// error at arg parse time (before any DB I/O).
func TestBatchMissingFileExit2(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runBatch(t, s, createCfg(), []string{"/nonexistent/path.jsonl"})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestBatchMissingPositionalExit2: no FILE argument → exit 2.
func TestBatchMissingPositionalExit2(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runBatch(t, s, createCfg(), nil)
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestBatchWithExternalParent: parent={"id": "proj-existing"}
// resolves against an existing task.
func TestBatchWithExternalParent(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-ext", "External", "", "open")
	path := writeBatchFile(t,
		`{"ref":"child","title":"Child","parent":{"id":"proj-ext"}}`+"\n")

	err, stdout, stderr := runBatch(t, s, createCfg(), []string{path})
	if err != nil {
		t.Fatalf("Batch: %v (stderr=%q)", err, stderr)
	}
	if !strings.Contains(stdout, `"ref":"child"`) {
		t.Errorf("stdout missing child pair; got %q", stdout)
	}
	var parent string
	if err := queryOne(t, dbPath, "SELECT parent FROM tasks WHERE title='Child'").Scan(&parent); err != nil {
		t.Fatalf("scan parent: %v", err)
	}
	if parent != "proj-ext" {
		t.Errorf("parent = %q, want proj-ext", parent)
	}
}

// TestBatchDepthExceededIsAtomic: one line whose depth would be 4
// blocks the entire batch in atomic mode.
func TestBatchDepthExceededIsAtomic(t *testing.T) {
	s, dbPath := testStore(t)
	path := writeBatchFile(t,
		`{"ref":"l1","title":"L1"}`+"\n"+
			`{"ref":"l2","title":"L2","parent":"l1"}`+"\n"+
			`{"ref":"l3","title":"L3","parent":"l2"}`+"\n"+
			`{"ref":"l4","title":"L4","parent":"l3"}`+"\n") // depth 4

	err, _, stderr := runBatch(t, s, createCfg(), []string{path})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(stderr, "depth_exceeded") {
		t.Errorf("stderr missing depth_exceeded; got %q", stderr)
	}
	var c int
	if err := queryOne(t, dbPath, "SELECT COUNT(*) FROM tasks").Scan(&c); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if c != 0 {
		t.Errorf("tasks = %d, want 0", c)
	}
}

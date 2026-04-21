//go:build integration

package main_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/store"
)

var questBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "quest-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	questBin = filepath.Join(tmp, "quest")
	build := exec.Command("go", "build", "-o", questBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestVersionJSON(t *testing.T) {
	cmd := exec.Command(questBin, "version")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("quest version: %v", err)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout not JSON: %v\nstdout: %s", err, out)
	}
	if got.Version == "" {
		t.Fatalf("version field empty; got %q", string(out))
	}
}

func TestVersionText(t *testing.T) {
	cmd := exec.Command(questBin, "--text", "version")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("quest version: %v", err)
	}
	s := string(out)
	if !strings.HasSuffix(s, "\n") {
		t.Fatalf("expected trailing newline; got %q", s)
	}
	line := strings.TrimRight(s, "\n")
	if line == "" {
		t.Fatalf("text version empty")
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("text mode emitted multiple lines: %q", s)
	}
}

// runInDir invokes the binary with cwd set to dir and returns (stdout,
// stderr, exit code). Exit 0 is forced to an exec.ExitError-less return
// so callers can assert against any code.
func runInDir(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(questBin, args...)
	cmd.Dir = dir
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if xerr, ok := err.(*exec.ExitError); ok {
			code = xerr.ExitCode()
		} else {
			t.Fatalf("quest %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// Happy-path init produces a .quest/ directory, a populated config.toml,
// a WAL-mode SQLite DB at schema version 1 with every table the initial
// migration creates, and a JSON output carrying both workspace + id_prefix.
// This exercises Layer 4 (stdout contract) and Layer 3 (DB shape after
// migrations run) together because both states are assertions on the
// same fresh workspace.
func TestInitHappyPath(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runInDir(t, dir, "init", "--prefix", "tst")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	var got struct {
		Workspace string `json:"workspace"`
		IDPrefix  string `json:"id_prefix"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", err, stdout)
	}
	if got.IDPrefix != "tst" {
		t.Errorf("id_prefix = %q, want tst", got.IDPrefix)
	}
	if filepath.Base(got.Workspace) != ".quest" {
		t.Errorf("workspace = %q, want basename .quest", got.Workspace)
	}
	if !filepath.IsAbs(got.Workspace) {
		t.Errorf("workspace = %q, want absolute path", got.Workspace)
	}

	cfgPath := filepath.Join(dir, ".quest", "config.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	cfgStr := string(body)
	if !strings.Contains(cfgStr, `id_prefix = "tst"`) {
		t.Errorf("config.toml missing id_prefix line: %q", cfgStr)
	}
	if !strings.Contains(cfgStr, `elevated_roles = ["planner"]`) {
		t.Errorf("config.toml missing elevated_roles line: %q", cfgStr)
	}

	dbPath := filepath.Join(dir, ".quest", "quest.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("quest.db missing: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	wantVersion := strconv.Itoa(store.SupportedSchemaVersion)
	if version != wantVersion {
		t.Errorf("schema_version = %q, want %q", version, wantVersion)
	}

	wantTables := []string{
		"dependencies", "history", "meta", "notes", "prs",
		"subtask_counter", "tags", "task_counter", "tasks",
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	var got2 []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got2 = append(got2, name)
	}
	sort.Strings(got2)
	if strings.Join(got2, ",") != strings.Join(wantTables, ",") {
		t.Errorf("tables = %v, want %v", got2, wantTables)
	}
}

func TestInitTextFormat(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runInDir(t, dir, "--text", "init", "--prefix", "tst")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr)
	}
	line := strings.TrimRight(stdout, "\n")
	if !strings.HasSuffix(stdout, "\n") {
		t.Errorf("text output missing trailing newline: %q", stdout)
	}
	if strings.ContainsAny(line, "{\":") {
		t.Errorf("text output looks like JSON: %q", stdout)
	}
	if filepath.Base(line) != ".quest" {
		t.Errorf("text output = %q, want absolute .quest path", line)
	}
	if !filepath.IsAbs(line) {
		t.Errorf("text output = %q, want absolute path", line)
	}
}

func TestInitBadPrefix(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		wantIn string
	}{
		{"too short", "a", "2-8 characters"},
		{"too long", "abcdefghi", "2-8 characters"},
		{"leading digit", "1ab", "must start with a letter"},
		{"hyphen", "a-b", "lowercase letters and digits only"},
		{"uppercase", "ABC", "lowercase letters and digits only"},
		{"reserved ref", "ref", "reserved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, stderr, code := runInDir(t, dir, "init", "--prefix", tc.prefix)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr=%q", code, stderr)
			}
			if !strings.Contains(stderr, "quest: usage_error:") {
				t.Errorf("stderr missing usage_error prefix: %q", stderr)
			}
			if !strings.Contains(stderr, tc.wantIn) {
				t.Errorf("stderr missing rule %q: %q", tc.wantIn, stderr)
			}
			if _, err := os.Stat(filepath.Join(dir, ".quest")); !os.IsNotExist(err) {
				t.Errorf(".quest/ should not exist after bad prefix; err=%v", err)
			}
		})
	}
}

func TestInitMissingPrefix(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runInDir(t, dir, "init")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "quest: usage_error:") {
		t.Errorf("stderr missing usage_error prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "--prefix is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr)
	}
}

func TestInitConflictExistingWorkspace(t *testing.T) {
	dir := t.TempDir()
	if _, stderr, code := runInDir(t, dir, "init", "--prefix", "tst"); code != 0 {
		t.Fatalf("first init exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code := runInDir(t, dir, "init", "--prefix", "tst")
	if code != 5 {
		t.Fatalf("second init exit = %d, want 5; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "quest: conflict:") {
		t.Errorf("stderr missing conflict prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr missing 'already exists' body: %q", stderr)
	}
}

// DiscoverRoot walks up from CWD — invoking init inside a nested
// directory of an existing workspace must refuse with exit 5.
func TestInitConflictAncestorWorkspace(t *testing.T) {
	dir := t.TempDir()
	if _, stderr, code := runInDir(t, dir, "init", "--prefix", "tst"); code != 0 {
		t.Fatalf("first init exit=%d stderr=%q", code, stderr)
	}
	nested := filepath.Join(dir, "sub")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	_, stderr, code := runInDir(t, nested, "init", "--prefix", "other")
	if code != 5 {
		t.Fatalf("nested init exit = %d, want 5; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr missing ancestor-conflict message: %q", stderr)
	}
}

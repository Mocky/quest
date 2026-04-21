//go:build integration

package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runWithEnv invokes the binary with cwd=dir plus extra environment
// variables (KEY=VALUE pairs appended to the parent env). Returns
// stdout, stderr, exit code. Same semantics as runInDir but lets
// tests set AGENT_ROLE / AGENT_TASK / AGENT_SESSION on the worker
// path.
func runWithEnv(t *testing.T, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(questBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
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

// runWithStdin is like runInDir but pipes content into stdin so @-
// resolution paths can be exercised end-to-end.
func runWithStdin(t *testing.T, dir, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(questBin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
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

// initWorkspace bootstraps a workspace via `quest init` so other
// contract tests can drive the binary against a real .quest/ on disk.
func initWorkspace(t *testing.T, dir, prefix string) {
	t.Helper()
	if _, stderr, code := runInDir(t, dir, "init", "--prefix", prefix); code != 0 {
		t.Fatalf("init exit = %d; stderr=%q", code, stderr)
	}
}

// TestGlobalFlagPositioning pins spec §Output & Error Conventions —
// `--text` is position-independent so `--text version` and
// `version --text` both work via cli.ParseGlobals.
func TestGlobalFlagPositioning(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runInDir(t, dir, "--text", "version")
	if code != 0 {
		t.Fatalf("--text version: exit = %d; stderr=%q", code, stderr)
	}
	line := strings.TrimRight(stdout, "\n")
	if line == "" || strings.Contains(line, "{") {
		t.Errorf("text version output looks wrong: %q", stdout)
	}
	stdout, stderr, code = runInDir(t, dir, "version", "--text")
	if code != 0 {
		t.Fatalf("version --text: exit = %d; stderr=%q", code, stderr)
	}
	line = strings.TrimRight(stdout, "\n")
	if line == "" || strings.Contains(line, "{") {
		t.Errorf("text version output looks wrong (post-command): %q", stdout)
	}
}

// TestStderrFramingOnUsageError pins the OBSERVABILITY.md §Output
// Contract: every error path emits `quest: <class>: <message>` on the
// first stderr line and `quest: exit N (<class>)` on the second.
func TestStderrFramingOnUsageError(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runInDir(t, dir, "init")
	if code != 2 {
		t.Fatalf("init (no prefix): exit = %d; want 2", code)
	}
	if !strings.Contains(stderr, "quest: usage_error: ") {
		t.Errorf("stderr first line missing usage_error prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "quest: exit 2 (usage_error)") {
		t.Errorf("stderr tail missing exit-2 framing: %q", stderr)
	}
	// Order: framing line should come last.
	idxClass := strings.Index(stderr, "quest: usage_error:")
	idxExit := strings.Index(stderr, "quest: exit 2")
	if idxExit < idxClass {
		t.Errorf("exit framing must follow class prefix; got: %q", stderr)
	}
}

// TestStderrFramingOnNotFound pins the framing for a not-found error
// class (exit 3) — the same `quest: <class>: <msg>` + `quest: exit N
// (<class>)` shape applies to every class, not just usage_error.
func TestStderrFramingOnNotFound(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	_, stderr, code := runInDir(t, dir, "show", "tst-zz9")
	if code != 3 {
		t.Fatalf("show: exit = %d; want 3", code)
	}
	if !strings.Contains(stderr, "quest: not_found: ") {
		t.Errorf("stderr missing not_found prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "quest: exit 3 (not_found)") {
		t.Errorf("stderr missing exit-3 framing: %q", stderr)
	}
}

// TestCancelTextFormat smoke-checks the text-mode rendering for
// `quest cancel`: spec doesn't pin text output as a contract, but a
// renderer regression that breaks the one-line-per-ID format silently
// breaks human-operator use. Asserts the cancelled task ID appears on
// its own line.
func TestCancelTextFormat(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	if _, stderr, code := runWithEnv(t, dir, []string{"AGENT_ROLE=planner"},
		"create", "--title", "Alpha"); code != 0 {
		t.Fatalf("create: exit=%d; stderr=%q", code, stderr)
	}
	stdout, stderr, code := runWithEnv(t, dir,
		[]string{"AGENT_ROLE=planner"},
		"--text", "cancel", "tst-01")
	if code != 0 {
		t.Fatalf("cancel: exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "tst-01") {
		t.Errorf("cancel text output missing target ID; got %q", stdout)
	}
}

// TestMoveTextFormat smoke-checks the rename rendering for
// `quest move` — `OLD → NEW` on each rename line. Asserts at least
// one line contains both old and new IDs.
func TestMoveTextFormat(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	planner := []string{"AGENT_ROLE=planner"}
	if _, stderr, code := runWithEnv(t, dir, planner, "create", "--title", "Alpha"); code != 0 {
		t.Fatalf("create alpha: %d %s", code, stderr)
	}
	if _, stderr, code := runWithEnv(t, dir, planner, "create", "--title", "Bravo"); code != 0 {
		t.Fatalf("create bravo: %d %s", code, stderr)
	}
	stdout, stderr, code := runWithEnv(t, dir, planner,
		"--text", "move", "tst-01", "--parent", "tst-02")
	if code != 0 {
		t.Fatalf("move: exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "tst-01") || !strings.Contains(stdout, "tst-02") {
		t.Errorf("move text output should reference both old + new IDs; got %q", stdout)
	}
}

// TestResetTextFormat pins the bare-id one-liner shape for `reset`.
func TestResetTextFormat(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	planner := []string{"AGENT_ROLE=planner"}
	if _, stderr, code := runWithEnv(t, dir, planner, "create", "--title", "Alpha"); code != 0 {
		t.Fatalf("create: %d %s", code, stderr)
	}
	worker := []string{"AGENT_ROLE=worker", "AGENT_TASK=tst-01", "AGENT_SESSION=sess-w1"}
	if _, stderr, code := runWithEnv(t, dir, worker, "accept", "tst-01"); code != 0 {
		t.Fatalf("accept: %d %s", code, stderr)
	}
	stdout, stderr, code := runWithEnv(t, dir, planner,
		"--text", "reset", "tst-01")
	if code != 0 {
		t.Fatalf("reset: exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "tst-01") {
		t.Errorf("reset text output missing id; got %q", stdout)
	}
}

// TestTagUntagTextFormat smoke-checks the post-state tag list output.
// Tag adds two tags; untag removes one; both `--text` calls emit the
// post-state list as part of stdout.
func TestTagUntagTextFormat(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	planner := []string{"AGENT_ROLE=planner"}
	if _, stderr, code := runWithEnv(t, dir, planner, "create", "--title", "Alpha"); code != 0 {
		t.Fatalf("create: %d %s", code, stderr)
	}
	stdout, stderr, code := runWithEnv(t, dir, planner,
		"--text", "tag", "tst-01", "go,auth")
	if code != 0 {
		t.Fatalf("tag: exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "tst-01") {
		t.Errorf("tag text output missing id; got %q", stdout)
	}
	stdout, stderr, code = runWithEnv(t, dir, planner,
		"--text", "untag", "tst-01", "go")
	if code != 0 {
		t.Fatalf("untag: exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "tst-01") {
		t.Errorf("untag text output missing id; got %q", stdout)
	}
}

// TestAtFileInput pins the @file resolution: a flag value beginning
// with `@` is treated as a file path; the file's contents replace the
// flag value before validation runs. Exercised end-to-end by writing
// a description to disk then `create --description @path`.
func TestAtFileInput(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	descPath := filepath.Join(dir, "desc.md")
	body := "Long-form description loaded from @file."
	if err := os.WriteFile(descPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write desc: %v", err)
	}

	planner := []string{"AGENT_ROLE=planner"}
	stdout, stderr, code := runWithEnv(t, dir, planner,
		"create", "--title", "T", "--description", "@"+descPath)
	if code != 0 {
		t.Fatalf("create exit = %d; stderr=%q", code, stderr)
	}
	// Ack carries only id; verify by reading the task back.
	if !strings.Contains(stdout, "tst-01") {
		t.Errorf("expected created id in stdout; got %q", stdout)
	}
	showOut, stderr, code := runWithEnv(t, dir, planner, "show", "tst-01")
	if code != 0 {
		t.Fatalf("show: exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(showOut, body) {
		t.Errorf("show output missing @file body %q; got %q", body, showOut)
	}
}

// TestAtStdinInput pins the @- resolution: a flag value of `@-` reads
// stdin once. Demonstrated end-to-end by completing a task with the
// debrief read from stdin.
func TestAtStdinInput(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	planner := []string{"AGENT_ROLE=planner"}
	if _, stderr, code := runWithEnv(t, dir, planner, "create", "--title", "T"); code != 0 {
		t.Fatalf("create: %d %s", code, stderr)
	}
	worker := []string{"AGENT_ROLE=worker", "AGENT_TASK=tst-01", "AGENT_SESSION=sess-w1"}
	if _, stderr, code := runWithEnv(t, dir, worker, "accept", "tst-01"); code != 0 {
		t.Fatalf("accept: %d %s", code, stderr)
	}

	debrief := "wrote handler, tests pass"
	stdout, stderr, code := runWithEnvStdin(t, dir, worker, debrief,
		"complete", "tst-01", "--debrief", "@-")
	if code != 0 {
		t.Fatalf("complete: exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "tst-01") {
		t.Errorf("expected id in stdout; got %q", stdout)
	}

	showOut, stderr, code := runWithEnv(t, dir, planner, "show", "tst-01")
	if code != 0 {
		t.Fatalf("show: %d %s", code, stderr)
	}
	if !strings.Contains(showOut, debrief) {
		t.Errorf("show output missing debrief from @-; got %q", showOut)
	}
}

// TestSecondStdinRejected pins the input.Resolver §Input Conventions
// rule: a second @- on the same invocation returns exit 2 with a
// pointer at the first consumer. Exercises the resolver through the
// real CLI binary so the wiring (handler constructs Resolver, threads
// stdin) is end-to-end verified.
func TestSecondStdinRejected(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir, "tst")
	planner := []string{"AGENT_ROLE=planner"}

	_, stderr, code := runWithEnvStdin(t, dir, planner, "x",
		"create", "--title", "T", "--description", "@-", "--context", "@-")
	if code != 2 {
		t.Fatalf("expected exit 2 on second @-; got %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "stdin already consumed") {
		t.Errorf("stderr missing stdin-double-consume message: %q", stderr)
	}
}

// runWithEnvStdin combines runWithEnv and runWithStdin. Kept as a
// distinct helper so the simpler wrappers stay readable.
func runWithEnvStdin(t *testing.T, dir string, env []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(questBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
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

// _ keeps the runWithStdin helper alive for tests that only need
// stdin without env overrides.
var _ = runWithStdin

//go:build eval

// Package eval houses the prompt-evaluation harness for quest's agent-facing
// prompts (docs/prompts/worker.md, docs/prompts/elevated.md). The eval is
// excluded from the normal test build because it spawns real LLM calls; run it
// explicitly with `make test-eval` or `go test -tags eval ./internal/eval/...`.
//
// See docs/prompt-eval.md for the design.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	promptElevatedPath = "../../docs/prompts/elevated.md"
	scenarioRoot       = "testdata"
	defaultModel       = "haiku"
	maxBudgetUSD       = 0.50
	runTimeout         = 5 * time.Minute
)

// resolveModel returns the model alias, defaulting to defaultModel. Override
// via QUEST_EVAL_MODEL=sonnet (or opus, etc.) to exercise the scenario against
// a different model without rebuilding.
func resolveModel() string {
	if m := os.Getenv("QUEST_EVAL_MODEL"); m != "" {
		return m
	}
	return defaultModel
}

// TestDiscoveredFromRegression is the seeded scenario from docs/prompt-eval.md.
// An agent is given an accepted task to fix one bug in foo.go; the file
// contains a second, unassigned bug. A correctly-prompted agent fixes the
// assigned bug, files a new task for the discovered one with
// `--discovered-from test-01`, and completes the assigned task.
//
// This is the qst-1e regression: an agent dropped the --discovered-from flag
// after misreading an unrelated error. The verifier catches both the outcome
// (no link in the export) and the trajectory (the create-without-discovered
// command shape).
func TestDiscoveredFromRegression(t *testing.T) {
	scenarioDir := filepath.Join(scenarioRoot, "discovered_from")

	var result agentResult
	defer reportScenario(t, "discovered_from", &result)

	workdir := t.TempDir()
	if err := setupSeed(scenarioDir, workdir); err != nil {
		t.Fatalf("seed setup: %v", err)
	}

	prompt, err := os.ReadFile(promptElevatedPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	task, err := os.ReadFile(filepath.Join(scenarioDir, "task.md"))
	if err != nil {
		t.Fatalf("read task: %v", err)
	}

	r, err := runAgent(t, workdir, string(prompt), string(task))
	if err != nil {
		t.Fatalf("agent run: %v", err)
	}
	result = *r

	if os.Getenv("QUEST_EVAL_VERBOSE") != "" {
		for i, c := range result.BashCalls {
			t.Logf("bash[%d]: %s", i, c)
		}
		t.Logf("final: %s", result.FinalText)
	}

	for _, err := range verifyOutcome(workdir) {
		t.Errorf("outcome: %v", err)
	}
	for _, err := range verifyTrajectory(result.BashCalls) {
		t.Errorf("trajectory: %v", err)
	}
}

// reportScenario emits a single-row summary of a scenario run. Called from a
// deferred closure so it sees the final t.Failed() state. With -v the row
// renders inline; without -v the test's `ok`/`FAIL` is enough on its own.
func reportScenario(t *testing.T, name string, r *agentResult) {
	t.Helper()
	verdict := "PASS"
	if t.Failed() {
		verdict = "FAIL"
	}
	t.Logf(
		"\n  %-20s %-8s %-7s %-15s %-9s %s\n  %-20s %-8s %-7d %-15s %-9s %s",
		"SCENARIO", "MODEL", "TURNS", "TOKENS(in/out)", "COST", "RESULT",
		name, resolveModel(), r.NumTurns,
		fmt.Sprintf("%d/%d", r.InputTokens, r.OutputTokens),
		fmt.Sprintf("$%.4f", r.CostUSD),
		verdict,
	)
}

// agentResult holds what we extracted from a stream-json run.
type agentResult struct {
	BashCalls    []string
	CostUSD      float64
	InputTokens  int
	OutputTokens int
	NumTurns     int
	FinalText    string
}

// setupSeed runs the scenario's seed/setup.sh against workdir.
func setupSeed(scenarioDir, workdir string) error {
	script, err := filepath.Abs(filepath.Join(scenarioDir, "seed", "setup.sh"))
	if err != nil {
		return err
	}
	cmd := exec.Command("bash", script, workdir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// runAgent spawns `claude -p` against workdir with the given system prompt and
// user-message task, parses the stream-json output, and returns the captured
// Bash invocations + usage data.
func runAgent(t *testing.T, workdir, systemPrompt, taskText string) (*agentResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	args := []string{
		"-p",
		"--system-prompt", systemPrompt,
		"--no-session-persistence",
		"--disable-slash-commands",
		"--output-format", "stream-json",
		"--verbose",
		"--model", resolveModel(),
		"--max-budget-usd", fmt.Sprintf("%.2f", maxBudgetUSD),
		"--allowedTools", "Bash,Read,Edit,Write",
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader(taskText)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Drain stderr in the background so it doesn't block; surface only on failure.
	stderrCh := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(stderr)
		stderrCh <- b
	}()

	result := &agentResult{}
	dec := json.NewDecoder(stdout)
	for {
		var event map[string]any
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Logf("stream decode error (continuing): %v", err)
			break
		}
		processEvent(event, result)
	}

	waitErr := cmd.Wait()
	stderrBytes := <-stderrCh
	if waitErr != nil {
		return result, fmt.Errorf("claude wait: %w\nstderr: %s", waitErr, stderrBytes)
	}
	return result, nil
}

// processEvent extracts Bash tool calls and result-event usage data.
func processEvent(event map[string]any, r *agentResult) {
	switch event["type"] {
	case "assistant":
		msg, _ := event["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			block, _ := b.(map[string]any)
			if block["type"] != "tool_use" {
				continue
			}
			if block["name"] != "Bash" {
				continue
			}
			input, _ := block["input"].(map[string]any)
			cmd, _ := input["command"].(string)
			if cmd != "" {
				r.BashCalls = append(r.BashCalls, cmd)
			}
		}
	case "result":
		if v, ok := event["total_cost_usd"].(float64); ok {
			r.CostUSD = v
		}
		if v, ok := event["num_turns"].(float64); ok {
			r.NumTurns = int(v)
		}
		if v, ok := event["result"].(string); ok {
			r.FinalText = v
		}
		if usage, ok := event["usage"].(map[string]any); ok {
			if v, ok := usage["input_tokens"].(float64); ok {
				r.InputTokens = int(v)
			}
			if v, ok := usage["output_tokens"].(float64); ok {
				r.OutputTokens = int(v)
			}
		}
	}
}

// verifyOutcome runs `quest export` against workdir and asserts the post-state
// predicates for the discovered_from scenario:
//   - exactly two tasks exist,
//   - test-01 reached `completed`,
//   - one task carries a discovered-from edge to test-01.
func verifyOutcome(workdir string) []error {
	var errs []error
	exportDir := filepath.Join(workdir, "quest-export")
	cmd := exec.Command("quest", "export", "--dir", exportDir)
	cmd.Dir = workdir
	if out, err := cmd.CombinedOutput(); err != nil {
		return append(errs, fmt.Errorf("quest export: %w: %s", err, out))
	}

	tasks, err := readExportedTasks(filepath.Join(exportDir, "tasks"))
	if err != nil {
		return append(errs, err)
	}
	if len(tasks) != 2 {
		errs = append(errs, fmt.Errorf("expected 2 tasks in export, got %d", len(tasks)))
	}

	var qst01 map[string]any
	var withLink map[string]any
	for _, t := range tasks {
		id, _ := t["id"].(string)
		if id == "test-01" {
			qst01 = t
		}
		if hasDependency(t, "discovered-from", "test-01") {
			withLink = t
		}
	}
	if qst01 == nil {
		errs = append(errs, fmt.Errorf("test-01 missing from export"))
	} else if status, _ := qst01["status"].(string); status != "completed" {
		errs = append(errs, fmt.Errorf("test-01 status: want completed, got %q", status))
	}
	if withLink == nil {
		errs = append(errs, fmt.Errorf("no task carries discovered-from -> test-01 (qst-1e regression)"))
	}
	return errs
}

func readExportedTasks(dir string) ([]map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read export tasks dir: %w", err)
	}
	var tasks []map[string]any
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var task map[string]any
		if err := json.Unmarshal(b, &task); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func hasDependency(task map[string]any, linkType, targetID string) bool {
	deps, _ := task["dependencies"].([]any)
	for _, d := range deps {
		dm, _ := d.(map[string]any)
		if lt, _ := dm["link_type"].(string); lt != linkType {
			continue
		}
		if id, _ := dm["id"].(string); id == targetID {
			return true
		}
	}
	return false
}

// verifyTrajectory walks the captured Bash invocations and checks anti-pattern
// and must-do predicates for the discovered_from scenario.
func verifyTrajectory(calls []string) []error {
	var errs []error

	// Anti-pattern: env-var prefix (agent self-identifying).
	for _, c := range calls {
		for _, prefix := range []string{"AGENT_ROLE=", "AGENT_TASK=", "AGENT_SESSION="} {
			if strings.Contains(c, prefix) {
				errs = append(errs, fmt.Errorf("env-var prefix %q in: %s", prefix, c))
			}
		}
	}

	// Anti-pattern: --text on quest commands.
	for _, c := range calls {
		if isQuestCmd(c) && containsFlag(c, "--text") {
			errs = append(errs, fmt.Errorf("--text on quest command: %s", c))
		}
	}

	// Anti-pattern: create-then-link instead of inline --discovered-from.
	for i := 0; i+1 < len(calls); i++ {
		a, b := calls[i], calls[i+1]
		if strings.Contains(a, "quest create") &&
			!containsFlag(a, "--discovered-from") &&
			strings.Contains(b, "quest link") &&
			containsFlag(b, "--discovered-from") {
			errs = append(errs, fmt.Errorf("create-then-link instead of inline --discovered-from:\n  1: %s\n  2: %s", a, b))
		}
	}

	// Must-do: at least one `quest create ... --discovered-from test-01`.
	hasInline := false
	for _, c := range calls {
		if strings.Contains(c, "quest create") &&
			containsFlag(c, "--discovered-from") &&
			strings.Contains(c, "test-01") {
			hasInline = true
			break
		}
	}
	if !hasInline {
		errs = append(errs, fmt.Errorf("no `quest create --discovered-from test-01` invocation observed (qst-1e regression)"))
	}

	return errs
}

func isQuestCmd(cmd string) bool {
	// Match `quest` as a token (not part of a path or another word).
	fields := strings.Fields(cmd)
	for _, f := range fields {
		if f == "quest" {
			return true
		}
	}
	return false
}

func containsFlag(cmd, flag string) bool {
	// Tokenize-ish — flags are space-separated.
	for _, f := range strings.Fields(cmd) {
		if f == flag || strings.HasPrefix(f, flag+"=") {
			return true
		}
	}
	return false
}

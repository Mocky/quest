// Package eval contains the prompt-evaluation harness and the benchmark log
// schema. The harness lives in eval_test.go (build-tagged with `eval`); this
// file defines the persistent benchmark format and the helpers shared by both
// the harness and the eval-compare tool (cmd/eval-compare/).
//
// See docs/prompt-eval.md for design.
package eval

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// BenchmarkEntry is one line in benchmarks.jsonl. Fields are flat by design
// so the file stays grep-friendly and an agent reading it can group / aggregate
// without unpacking nested structures.
type BenchmarkEntry struct {
	Timestamp    string  `json:"ts"`
	Scenario     string  `json:"scenario"`
	Model        string  `json:"model"`
	PromptPath   string  `json:"prompt_path"`
	PromptSHA    string  `json:"prompt_sha"`
	PromptTokens int     `json:"prompt_tokens"`
	Turns        int     `json:"turns"`
	Input        int     `json:"input"`
	Output       int     `json:"output"`
	CacheRead    int     `json:"cache_read"`
	CacheWrite   int     `json:"cache_write"`
	CostUSD      float64 `json:"cost_usd"`
	Pass         bool    `json:"pass"`
	GitHead      string  `json:"git_head"`
}

// AppendBenchmark writes one entry as a JSON line to path, creating the file
// if it doesn't exist.
func AppendBenchmark(path string, entry BenchmarkEntry) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(entry)
}

// ReadBenchmarks loads every entry from path. A missing file returns nil with
// no error so callers can treat "no history yet" as a valid empty input.
func ReadBenchmarks(path string) ([]BenchmarkEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var entries []BenchmarkEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e BenchmarkEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("parse benchmarks line: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

// PromptSHA returns the hex SHA-256 of a prompt file's contents.
func PromptSHA(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// EstimatePromptTokens is a heuristic English-markdown token count
// (word_count * 1.3). Accurate to ~5%; suitable for tracking changes, not
// for billing.
func EstimatePromptTokens(text string) int {
	return int(float64(len(strings.Fields(text))) * 1.3)
}

// GitHead returns the short SHA of HEAD or an empty string on error.
func GitHead() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// NowUTC returns now in RFC 3339 / second precision UTC, matching quest's
// timestamp convention.
func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// RepoRoot walks up from CWD looking for a `.git` entry. Both the harness
// (which runs from internal/eval/) and the compare tool (which usually runs
// from repo root) need a stable anchor for benchmarks.jsonl.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git found above %s", dir)
		}
		dir = parent
	}
}

// BenchmarkLogPath resolves to the canonical benchmarks.jsonl location
// regardless of the caller's CWD. This file is committed to git and is the
// official record; only `eval-promote` writes here.
func BenchmarkLogPath() (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "internal", "eval", "benchmarks.jsonl"), nil
}

// ScratchLogPath resolves to internal/eval/scratch.jsonl. This file is
// gitignored — it is the harness's append-only working log. `eval-promote`
// reads it, filters to entries matching the current prompt files on disk,
// appends those entries to benchmarks.jsonl, then truncates scratch.
func ScratchLogPath() (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "internal", "eval", "scratch.jsonl"), nil
}

// IsBenchmarked reports whether benchmarks.jsonl already contains an entry
// matching the given (scenario, model, promptPath, sha) tuple. Used by the
// harness's skip-if-benchmarked path so `eval-changed` skips scenarios whose
// current prompt SHA has already been officially recorded.
func IsBenchmarked(scenario, model, promptPath, sha string) (bool, error) {
	logPath, err := BenchmarkLogPath()
	if err != nil {
		return false, err
	}
	entries, err := ReadBenchmarks(logPath)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Scenario == scenario && e.Model == model && e.PromptPath == promptPath && e.PromptSHA == sha {
			return true, nil
		}
	}
	return false, nil
}

// TruncateScratch empties the scratch log. Called from `eval-promote` after a
// successful promote.
func TruncateScratch() error {
	path, err := ScratchLogPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Truncate(path, 0)
}

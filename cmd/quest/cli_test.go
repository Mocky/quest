//go:build integration

package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	cmd := exec.Command(questBin, "--format", "text", "version")
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

package input_test

import (
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/input"
)

func TestResolveBareStringPassesThrough(t *testing.T) {
	r := input.NewResolver(strings.NewReader(""))
	got, err := r.Resolve("--debrief", "hello world")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestResolveStdinHappyPath(t *testing.T) {
	r := input.NewResolver(strings.NewReader("report body"))
	got, err := r.Resolve("--debrief", "@-")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "report body" {
		t.Errorf("got %q, want %q", got, "report body")
	}
}

func TestResolveSecondStdinRejected(t *testing.T) {
	r := input.NewResolver(strings.NewReader("first"))
	if _, err := r.Resolve("--debrief", "@-"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	_, err := r.Resolve("--note", "@-")
	if err == nil {
		t.Fatalf("second @-: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "stdin already consumed by --debrief") {
		t.Errorf("err = %q, want mentions first consumer", err.Error())
	}
}

func TestResolveFileHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debrief.md")
	if err := os.WriteFile(path, []byte("file body"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := input.NewResolver(strings.NewReader(""))
	got, err := r.Resolve("--debrief", "@"+path)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "file body" {
		t.Errorf("got %q, want %q", got, "file body")
	}
}

func TestResolveMissingFileIsUsage(t *testing.T) {
	r := input.NewResolver(strings.NewReader(""))
	_, err := r.Resolve("--debrief", "@/nonexistent/path/xyz")
	if err == nil {
		t.Fatalf("Resolve: got nil, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "--debrief:") {
		t.Errorf("err = %q, want leading flag token", err.Error())
	}
}

func TestResolveOversizedStdinRejected(t *testing.T) {
	// Build a body one byte above the cap.
	body := strings.Repeat("a", input.MaxBytes+1)
	r := input.NewResolver(strings.NewReader(body))
	_, err := r.Resolve("--debrief", "@-")
	if err == nil {
		t.Fatalf("got nil, want oversized ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "exceeds 1 MiB limit") {
		t.Errorf("err = %q, want mentions limit", err.Error())
	}
}

func TestResolveOversizedFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", input.MaxBytes+1)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := input.NewResolver(strings.NewReader(""))
	_, err := r.Resolve("--description", "@"+path)
	if err == nil {
		t.Fatalf("got nil, want oversized ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "exceeds 1 MiB limit") {
		t.Errorf("err = %q, want mentions limit", err.Error())
	}
}

func TestResolveStdinBelowCapAccepted(t *testing.T) {
	body := strings.Repeat("b", input.MaxBytes)
	r := input.NewResolver(strings.NewReader(body))
	got, err := r.Resolve("--debrief", "@-")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != input.MaxBytes {
		t.Errorf("got len=%d, want %d", len(got), input.MaxBytes)
	}
}

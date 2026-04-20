//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/cli"
	"github.com/mocky/quest/internal/config"
)

// setupWorkspace creates a .quest/ directory with a valid config.toml
// so the dispatcher's workspace + validate + store open + migrate path
// runs end to end.
func setupWorkspace(t *testing.T, prefix, role string) config.Config {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".quest"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return config.Config{
		Workspace: config.WorkspaceConfig{
			Root:          root,
			DBPath:        filepath.Join(root, ".quest", "quest.db"),
			IDPrefix:      prefix,
			ElevatedRoles: []string{"planner"},
		},
		Agent:  config.AgentConfig{Role: role},
		Log:    config.LogConfig{Level: "warn", OTELLevel: "info"},
		Output: config.OutputConfig{Format: "json"},
	}
}

func runExecute(args []string, cfg config.Config) (int, string, string) {
	var out, errb bytes.Buffer
	exit := cli.Execute(context.Background(), cfg, args, strings.NewReader(""), &out, &errb)
	return exit, out.String(), errb.String()
}

// TestExecuteExportDefaultDirWorkspaceRelative pins phase-11-export.md:
// "A planner running `quest export` from `<workspace>/src/` writes the
// archive to `<workspace>/quest-export/`, not `<workspace>/src/
// quest-export/`." Verified end-to-end via cli.Execute so the CWD ≠
// workspace-root path runs the real dispatch + handler wiring.
func TestExecuteExportDefaultDirWorkspaceRelative(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "planner")
	sub := filepath.Join(cfg.Workspace.Root, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(sub); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	exit, stdout, stderr := runExecute([]string{"export"}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", exit, stderr)
	}
	var ack struct {
		Dir string `json:"dir"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	wantDir := filepath.Join(cfg.Workspace.Root, "quest-export")
	if ack.Dir != wantDir {
		t.Errorf("ack.Dir = %q, want %q (workspace-root-relative, not CWD-relative)", ack.Dir, wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("expected archive at %s: %v", wantDir, err)
	}
	stale := filepath.Join(sub, "quest-export")
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("unexpected archive in CWD at %s", stale)
	}
}

// Happy-path dispatch: the store opens, migrations run, and the show
// handler reaches its store lookup. With no task seeded, the handler
// returns ErrNotFound (exit 3). Proves steps 3-7 of the dispatch
// sequence execute in order against a real SQLite workspace.
func TestExecuteDispatchReachesHandler(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "worker")

	exit, _, stderr := runExecute([]string{"show", "proj-01"}, cfg)
	if exit != 3 {
		t.Fatalf("exit = %d, want 3 (not_found)", exit)
	}
	if !strings.Contains(stderr, "quest: not_found:") {
		t.Fatalf("stderr missing not_found prefix: %q", stderr)
	}

	// Migration must have landed — meta.schema_version is populated.
	db, err := sql.Open("sqlite", "file:"+cfg.Workspace.DBPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if v != "2" {
		t.Fatalf("schema_version = %q, want 2", v)
	}
}

// Schema too new: if the stored schema version exceeds the binary's
// supported version, Execute exits 1 with the spec-pinned message.
// Proves step 5 correctly refuses forward migrations.
func TestExecuteRejectsNewerSchema(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "planner")

	// Seed a meta table with a schema_version beyond what the binary
	// supports — the migration runner must refuse.
	db, err := sql.Open("sqlite", "file:"+cfg.Workspace.DBPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', '99')`); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	_ = db.Close()

	exit, _, stderr := runExecute([]string{"show", "proj-01"}, cfg)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1 (general_failure)", exit)
	}
	if !strings.Contains(stderr, "newer than this binary supports") {
		t.Errorf("stderr missing schema-too-new message: %q", stderr)
	}
}

// Second invocation: schema is already current, so no migration runs
// but the handler is still dispatched. Proves step 5's "from ==
// supported" branch skips migration cleanly.
func TestExecuteSecondInvocationSkipsMigration(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "worker")

	// First invocation primes the DB; the show handler exits 3 because
	// no task row exists yet.
	if exit, _, _ := runExecute([]string{"show", "proj-01"}, cfg); exit != 3 {
		t.Fatalf("first exit = %d, want 3 (not_found)", exit)
	}

	// Second invocation — schema_version is already at SupportedSchemaVersion.
	exit, _, stderr := runExecute([]string{"show", "proj-01"}, cfg)
	if exit != 3 {
		t.Fatalf("second exit = %d, want 3 (not_found)", exit)
	}
	if !strings.Contains(stderr, "quest: not_found:") {
		t.Errorf("expected not_found prefix, got: %q", stderr)
	}
}

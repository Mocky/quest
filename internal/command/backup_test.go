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

// runBackup drives command.Backup against a real store and returns
// (err, stdout, stderr).
func runBackup(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Backup(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// backupCfg is plannerCfg with Workspace.Root set so the sidecar
// source path resolves to the test's scratch workspace.
func backupCfg(t *testing.T, workspaceRoot string) config.Config {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".quest"), 0o755); err != nil {
		t.Fatalf("mkdir .quest: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workspaceRoot, ".quest", "config.toml"),
		[]byte("elevated_roles = [\"planner\"]\nid_prefix = \"proj\"\n"),
		0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return config.Config{
		Workspace: config.WorkspaceConfig{Root: workspaceRoot, ElevatedRoles: []string{"planner"}},
		Agent:     config.AgentConfig{Role: "planner", Session: "sess-p1"},
		Output:    config.OutputConfig{},
	}
}

// TestBackupFailsWithoutTo pins that `--to` is required; error is
// ErrUsage and stderr mentions the flag.
func TestBackupFailsWithoutTo(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runBackup(t, s, backupCfg(t, t.TempDir()), nil)
	if err == nil {
		t.Fatalf("got nil err, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Errorf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "--to is required") {
		t.Errorf("err does not mention --to: %v", err)
	}
}

// TestBackupFailsWithoutParentDir pins spec §`quest backup`: parent
// directory must exist; quest backup does not create it.
func TestBackupFailsWithoutParentDir(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runBackup(t, s, backupCfg(t, t.TempDir()),
		[]string{"--to", "/does/not/exist/nowhere/snap.db"})
	if err == nil {
		t.Fatalf("got nil err, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Errorf("err = %v, want wraps ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "parent directory does not exist") {
		t.Errorf("err does not mention parent dir: %v", err)
	}
}

// TestBackupUnexpectedPositional pins that trailing positionals are
// rejected with ErrUsage.
func TestBackupUnexpectedPositional(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runBackup(t, s, backupCfg(t, t.TempDir()),
		[]string{"--to", filepath.Join(t.TempDir(), "snap.db"), "extra"})
	if err == nil {
		t.Fatalf("got nil err, want ErrUsage")
	}
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Errorf("err = %v, want wraps ErrUsage", err)
	}
}

// TestBackupWritesDBAndSidecar is the core happy-path assertion:
// both the db and db.config.toml files land in the output directory,
// the ack fields are populated, and the schema version matches
// store.SupportedSchemaVersion.
func TestBackupWritesDBAndSidecar(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")
	seedMinimalTask(t, s, "proj-a2", "Beta")

	workspace := t.TempDir()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")
	err, stdout, _ := runBackup(t, s, backupCfg(t, workspace), []string{"--to", outPath})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	var ack backupAck
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if ack.DB != outPath {
		t.Errorf("ack.DB = %q, want %q", ack.DB, outPath)
	}
	if ack.Config != outPath+".config.toml" {
		t.Errorf("ack.Config = %q, want %q.config.toml", ack.Config, outPath)
	}
	if ack.SchemaVersion != store.SupportedSchemaVersion {
		t.Errorf("ack.SchemaVersion = %d, want %d", ack.SchemaVersion, store.SupportedSchemaVersion)
	}
	if ack.Bytes <= 0 {
		t.Errorf("ack.Bytes = %d, want > 0", ack.Bytes)
	}
	if !filepath.IsAbs(ack.DB) {
		t.Errorf("ack.DB not absolute: %q", ack.DB)
	}
	if !filepath.IsAbs(ack.Config) {
		t.Errorf("ack.Config not absolute: %q", ack.Config)
	}
	if _, serr := os.Stat(outPath); serr != nil {
		t.Errorf("db file missing: %v", serr)
	}
	if _, serr := os.Stat(outPath + ".config.toml"); serr != nil {
		t.Errorf("config sidecar missing: %v", serr)
	}
}

// TestBackupSnapshotRestorable closes the loop: reopen the snapshot
// via store.Open and assert the seeded tasks are present. Proves the
// snapshot actually restores.
func TestBackupSnapshotRestorable(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	workspace := t.TempDir()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")
	if err, _, _ := runBackup(t, s, backupCfg(t, workspace), []string{"--to", outPath}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	copy, err := store.Open(outPath)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	defer copy.Close()
	task, err := copy.GetTask(context.Background(), "proj-a1")
	if err != nil {
		t.Fatalf("GetTask on snapshot: %v", err)
	}
	if task.Title != "Alpha" {
		t.Errorf("task.Title = %q, want Alpha", task.Title)
	}
}

// TestBackupSidecarContentMatchesSource compares the sidecar bytes
// to the source config.toml byte-for-byte.
func TestBackupSidecarContentMatchesSource(t *testing.T) {
	s, _ := testStore(t)

	workspace := t.TempDir()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")
	cfg := backupCfg(t, workspace)
	if err, _, _ := runBackup(t, s, cfg, []string{"--to", outPath}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	src, err := os.ReadFile(filepath.Join(workspace, ".quest", "config.toml"))
	if err != nil {
		t.Fatalf("read source config: %v", err)
	}
	dst, err := os.ReadFile(outPath + ".config.toml")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if !bytes.Equal(src, dst) {
		t.Errorf("sidecar bytes differ from source")
	}
}

// TestBackupOverwritesExistingDestination pins the spec idempotency
// note: a backup to an existing PATH replaces the file with a fresh
// snapshot.
func TestBackupOverwritesExistingDestination(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	workspace := t.TempDir()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")

	// Pre-seed with a dummy file.
	if err := os.WriteFile(outPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile stale: %v", err)
	}
	if err, _, _ := runBackup(t, s, backupCfg(t, workspace), []string{"--to", outPath}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	copy, err := store.Open(outPath)
	if err != nil {
		t.Fatalf("reopen overwritten snapshot: %v", err)
	}
	defer copy.Close()
	v, err := copy.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion: %v", err)
	}
	if v != store.SupportedSchemaVersion {
		t.Errorf("schema = %d, want %d (dummy file was not replaced)", v, store.SupportedSchemaVersion)
	}
}

// TestBackupMissingSidecarSourceRemovesPartialSnapshot pins the "both
// or neither" recovery-unit contract. If the source config.toml is
// missing, the handler must exit ErrGeneral and clean up the
// partially written snapshot so a retry sees a clean slate.
func TestBackupMissingSidecarSourceRemovesPartialSnapshot(t *testing.T) {
	s, _ := testStore(t)

	workspace := t.TempDir()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")

	// Set up the workspace config.toml then delete it so the snapshot
	// step succeeds but the sidecar copy fails.
	cfg := backupCfg(t, workspace)
	if err := os.Remove(filepath.Join(workspace, ".quest", "config.toml")); err != nil {
		t.Fatalf("remove config.toml: %v", err)
	}

	err, _, _ := runBackup(t, s, cfg, []string{"--to", outPath})
	if err == nil {
		t.Fatalf("Backup: got nil err, want ErrGeneral")
	}
	if !stderrors.Is(err, errors.ErrGeneral) {
		t.Errorf("err = %v, want wraps ErrGeneral", err)
	}
	if !strings.Contains(err.Error(), "sidecar write failed") {
		t.Errorf("err does not mention sidecar: %v", err)
	}
	if _, serr := os.Stat(outPath); serr == nil {
		t.Errorf("partial snapshot not removed: %s still exists", outPath)
	} else if !os.IsNotExist(serr) {
		t.Errorf("unexpected stat err: %v", serr)
	}
	if _, serr := os.Stat(outPath + ".config.toml"); serr == nil {
		t.Errorf("partial sidecar not removed")
	}
}

// TestBackupTextFormatOutputsPathOnly pins the text-mode contract:
// stdout is the bare absolute db path followed by a newline. Mirrors
// the `quest init` / `quest export` text-mode convention.
func TestBackupTextFormatOutputsPathOnly(t *testing.T) {
	s, _ := testStore(t)

	workspace := t.TempDir()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")
	cfg := backupCfg(t, workspace)
	cfg.Output.Text = true
	err, stdout, _ := runBackup(t, s, cfg, []string{"--to", outPath})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	got := strings.TrimRight(stdout, "\n")
	if got != outPath {
		t.Errorf("stdout = %q, want %q", got, outPath)
	}
}

// TestBackupJSONFormatAllFieldsPresent pins spec §`quest backup`:
// "all four fields are always present".
func TestBackupJSONFormatAllFieldsPresent(t *testing.T) {
	s, _ := testStore(t)
	seedMinimalTask(t, s, "proj-a1", "Alpha")

	workspace := t.TempDir()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")
	err, stdout, _ := runBackup(t, s, backupCfg(t, workspace), []string{"--to", outPath})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	var m map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &m); jerr != nil {
		t.Fatalf("stdout not JSON: %v", jerr)
	}
	for _, k := range []string{"db", "config", "schema_version", "bytes"} {
		if _, ok := m[k]; !ok {
			t.Errorf("field %q missing from ack", k)
		}
	}
}

// backupAck mirrors command.backupAck to decode the JSON envelope in
// tests without exporting the package-private type.
type backupAck struct {
	DB            string `json:"db"`
	Config        string `json:"config"`
	SchemaVersion int    `json:"schema_version"`
	Bytes         int64  `json:"bytes"`
}

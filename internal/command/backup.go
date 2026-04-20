package command

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// backupAck is the stdout envelope for `quest backup --to PATH`. All
// four fields are always present per spec §`quest backup`. Text mode
// emits the bare absolute db path, mirroring the `quest init` and
// `quest export` text-mode convention.
type backupAck struct {
	DB            string `json:"db"`
	Config        string `json:"config"`
	SchemaVersion int    `json:"schema_version"`
	Bytes         int64  `json:"bytes"`
}

// Backup handles `quest backup --to PATH`. Elevated-only (gated by the
// dispatcher). Writes a transaction-consistent DB snapshot to PATH and
// a sidecar copy of .quest/config.toml to PATH.config.toml. See spec
// §quest backup and §Backup & Recovery. The pair is the recovery unit:
// if the sidecar fails, the partial snapshot is removed and the
// command exits 1 so a retry sees a clean slate.
func Backup(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin

	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "output path for the database snapshot (required)")
	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("backup: %s: %w", err.Error(), errors.ErrUsage)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("backup: unexpected positional arguments: %w", errors.ErrUsage)
	}
	if *to == "" {
		return fmt.Errorf("backup: --to is required: %w", errors.ErrUsage)
	}

	absDB, err := filepath.Abs(*to)
	if err != nil {
		return fmt.Errorf("backup: %s: %w", err.Error(), errors.ErrGeneral)
	}
	// Parent-directory existence is spec-pinned: not created.
	if fi, serr := os.Stat(filepath.Dir(absDB)); serr != nil || !fi.IsDir() {
		return fmt.Errorf("backup: parent directory does not exist: %s: %w", filepath.Dir(absDB), errors.ErrUsage)
	}
	absCfg := absDB + ".config.toml"

	bytes, err := s.Snapshot(ctx, absDB)
	if err != nil {
		return err
	}

	// Sidecar copy. On any failure, remove BOTH the snapshot and any
	// partial sidecar the failed write may have left behind — the pair
	// is the recovery unit per spec §Backup & Recovery and a retry
	// must see a clean slate. Remove calls are best-effort.
	srcCfg := filepath.Join(cfg.Workspace.Root, ".quest", "config.toml")
	if err := copyFile(srcCfg, absCfg); err != nil {
		_ = os.Remove(absDB)
		_ = os.Remove(absCfg)
		return fmt.Errorf("backup: sidecar write failed: %s: %w", err.Error(), errors.ErrGeneral)
	}

	// Read schema version post-snapshot so the ack reports exactly
	// what an operator restoring the file will observe. The race is
	// theoretical (migrations serialize on the write lock and only run
	// at startup), but the round-trip is cheap insurance.
	version, err := s.CurrentSchemaVersion(ctx)
	if err != nil {
		return err
	}

	if cfg.Output.Format == "text" {
		fmt.Fprintln(stdout, absDB)
		return nil
	}
	ack := backupAck{DB: absDB, Config: absCfg, SchemaVersion: version, Bytes: bytes}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "")
	return enc.Encode(ack)
}

// copyFile is a tiny stdlib helper — the only use in the package, so
// it stays private here. Promote to internal/util if a second use
// appears.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

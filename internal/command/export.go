package command

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/export"
	"github.com/mocky/quest/internal/store"
)

// exportAck is the stdout envelope for `quest export`. Spec §`quest
// export` does not pin an output shape; agents and operators need
// machine-readable confirmation of where the archive landed and how
// much it contains, so the handler returns counts (dir, tasks,
// debriefs, history_entries). Fields are always present per spec
// §Output & Error Conventions ("all fields always present"). Text mode
// emits the bare absolute path to match the `quest init` convention.
type exportAck struct {
	Dir            string `json:"dir"`
	Tasks          int    `json:"tasks"`
	Debriefs       int    `json:"debriefs"`
	HistoryEntries int    `json:"history_entries"`
}

// Export handles `quest export [--dir PATH]`. Default output is
// `<workspace>/quest-export/` — always a sibling of `.quest/` per spec
// §`quest export` ("default: `./quest-export/`" is spec-relative to
// the workspace root, not CWD, so running from a subdirectory still
// places the archive beside .quest). When --dir is provided and
// relative, it is resolved against CWD per standard CLI convention.
func Export(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin

	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dirFlag := fs.String("dir", "", "output directory (default: <workspace>/quest-export)")
	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("export: %s: %w", err.Error(), errors.ErrUsage)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("export: unexpected positional arguments: %w", errors.ErrUsage)
	}

	outDir := *dirFlag
	if outDir == "" {
		outDir = filepath.Join(cfg.Workspace.Root, "quest-export")
	}
	absDir, err := filepath.Abs(outDir)
	if err != nil {
		return fmt.Errorf("export: %s: %w", err.Error(), errors.ErrGeneral)
	}

	summary, err := export.Write(ctx, s, absDir)
	if err != nil {
		return err
	}

	if cfg.Output.Format == "text" {
		fmt.Fprintln(stdout, summary.Dir)
		return nil
	}
	ack := exportAck{
		Dir:            summary.Dir,
		Tasks:          summary.TaskCount,
		Debriefs:       summary.DebriefCount,
		HistoryEntries: summary.HistoryEntries,
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "")
	return enc.Encode(ack)
}

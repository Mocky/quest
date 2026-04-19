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
	"github.com/mocky/quest/internal/ids"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

const initConfigTemplate = `# Role gating — AGENT_ROLE values that unlock elevated commands.
elevated_roles = ["planner"]

# Task IDs (immutable for this project's lifetime).
id_prefix = "%s"
`

// Init bootstraps a quest workspace in the current directory. It is
// dispatched with RequiresWorkspace=false (the dispatcher skips the
// workspace presence check, config.Validate, and the store open +
// migrate pre-handler step), so this handler owns every side effect:
// prefix validation, conflict detection via config.DiscoverRoot,
// directory creation, config.toml write, DB open + migrate. The
// dispatcher supplies a nil store; init opens its own and is the only
// handler-path caller of telemetry.WrapStore per OTEL.md §8.8.
func Init(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = s
	_ = stdin

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prefix := fs.String("prefix", "", "task ID prefix for this project (2-8 lowercase chars, must start with a letter)")
	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("init: %s: %w", err.Error(), errors.ErrUsage)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("init: unexpected positional arguments: %w", errors.ErrUsage)
	}
	if *prefix == "" {
		return fmt.Errorf("init: --prefix is required: %w", errors.ErrUsage)
	}
	if err := ids.ValidatePrefix(*prefix); err != nil {
		return fmt.Errorf("init: --prefix: %s: %w", err.Error(), errors.ErrUsage)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("init: %s: %w", err.Error(), errors.ErrGeneral)
	}

	existing, derr := config.DiscoverRoot(cwd)
	switch {
	case derr == nil:
		return fmt.Errorf(".quest/ already exists at %s: %w", filepath.Join(existing, ".quest"), errors.ErrConflict)
	case !stderrors.Is(derr, config.ErrNoWorkspace):
		return fmt.Errorf("init: %s: %w", derr.Error(), errors.ErrGeneral)
	}

	questDir := filepath.Join(cwd, ".quest")
	if err := os.Mkdir(questDir, 0o755); err != nil {
		return fmt.Errorf("init: %s: %w", err.Error(), errors.ErrGeneral)
	}
	cfgPath := filepath.Join(questDir, "config.toml")
	cfgBody := fmt.Sprintf(initConfigTemplate, *prefix)
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		return fmt.Errorf("init: %s: %w", err.Error(), errors.ErrGeneral)
	}

	dbPath := filepath.Join(questDir, "quest.db")
	opened, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer opened.Close()
	wrapped := telemetry.WrapStore(opened)
	from, err := wrapped.CurrentSchemaVersion(ctx)
	if err != nil {
		return err
	}
	migCtx, end := telemetry.MigrateSpan(ctx, from, store.SupportedSchemaVersion)
	applied, merr := store.Migrate(migCtx, wrapped)
	end(applied, merr)
	if merr != nil {
		return merr
	}

	absQuestDir, err := filepath.Abs(questDir)
	if err != nil {
		return fmt.Errorf("init: %s: %w", err.Error(), errors.ErrGeneral)
	}

	if cfg.Output.Format == "text" {
		fmt.Fprintln(stdout, absQuestDir)
		return nil
	}
	payload := struct {
		Workspace string `json:"workspace"`
		IDPrefix  string `json:"id_prefix"`
	}{Workspace: absQuestDir, IDPrefix: *prefix}
	if err := json.NewEncoder(stdout).Encode(payload); err != nil {
		return fmt.Errorf("init: encode json: %s: %w", err.Error(), errors.ErrGeneral)
	}
	return nil
}

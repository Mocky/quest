package command

import (
	"context"
	stderrors "errors"
	"flag"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// Deps lists a task's outgoing dependency edges with the target's title
// and status denormalized. `ID` is required.
func Deps(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	_ = stdin

	positional, flagArgs := splitLeadingPositional(args)
	fs := newFlagSet("deps")
	fs.SetOutput(stderr)
	if perr := fs.Parse(flagArgs); perr != nil {
		if stderrors.Is(perr, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("deps: %s: %w", perr.Error(), errors.ErrUsage)
	}
	positional = append(positional, fs.Args()...)
	if len(positional) == 0 || positional[0] == "" {
		return fmt.Errorf("deps: task ID required: %w", errors.ErrUsage)
	}
	if len(positional) > 1 {
		return fmt.Errorf("deps: unexpected positional arguments: %w", errors.ErrUsage)
	}
	id := positional[0]

	task, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	telemetry.RecordTaskContext(ctx, task.ID, task.Tier, task.Type)

	ctx2, end := telemetry.StoreSpan(ctx, "quest.store.traverse")
	defer func() { end(err) }()
	deps, err := s.GetDependencies(ctx2, id)
	if err != nil {
		return err
	}
	telemetry.RecordQueryResult(ctx, "deps", len(deps), telemetry.QueryFilter{})

	if deps == nil {
		deps = []store.Dependency{}
	}
	if cfg.Output.Format == "text" {
		return emitDepsText(stdout, deps)
	}
	return output.Emit(stdout, cfg.Output.Format, deps)
}

// emitDepsText writes a fixed-width table with the dependency list.
// Empty dependency lists still emit the header row so the contract
// between text and JSON (JSON emits []) is symmetric.
func emitDepsText(w io.Writer, deps []store.Dependency) error {
	cols := []output.Column{
		{Name: "TARGET", Width: 20},
		{Name: "TYPE", Width: 16},
		{Name: "STATUS", Width: 10},
		{Name: "TITLE", Width: 40},
	}
	rows := make([][]string, 0, len(deps))
	for _, d := range deps {
		rows = append(rows, []string{d.ID, d.LinkType, d.Status, d.Title})
	}
	return output.Table(w, cols, rows)
}

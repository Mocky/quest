package command

import (
	"context"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// batchArgs captures the parsed command-line flags. FILE is a
// required positional; --partial-ok toggles the spec-pinned
// partial-success mode.
type batchArgs struct {
	File      string
	PartialOK bool
}

// parseBatchArgs handles the CLI shape: exactly one positional
// FILE, plus optional --partial-ok. The flag package needs the
// positional to come AFTER flags, so we accept both orders by
// splitting the leading positional before delegating to flag.Parse.
func parseBatchArgs(stderr io.Writer, args []string) (batchArgs, error) {
	// Pull off a leading non-flag arg if present (the FILE).
	positional, rest := splitLeadingPositional(args)
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	partialOK := fs.Bool("partial-ok", false, "create tasks that passed validation even when other lines failed")
	if err := fs.Parse(rest); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return batchArgs{}, nil
		}
		return batchArgs{}, fmt.Errorf("batch: %s: %w", err.Error(), errors.ErrUsage)
	}
	positional = append(positional, fs.Args()...)
	if len(positional) == 0 {
		return batchArgs{}, fmt.Errorf("batch: FILE argument is required: %w", errors.ErrUsage)
	}
	if len(positional) > 1 {
		return batchArgs{}, fmt.Errorf("batch: unexpected positional arguments: %w", errors.ErrUsage)
	}
	return batchArgs{File: positional[0], PartialOK: *partialOK}, nil
}

// Batch is the `quest batch FILE [--partial-ok]` handler. Phase 1
// runs outside the transaction; phases 2–4 plus the creation pass
// share one BEGIN IMMEDIATE write-lock so the validation view and
// the insert pass see the same committed graph snapshot.
//
// Output split:
//   - stderr: JSONL of every BatchError, written as each phase
//     completes; the dispatcher appends the `quest: <class>: …`
//     tail once Batch returns.
//   - stdout: JSONL of every successfully-created task's
//     `{"ref": "...", "id": "..."}` pair (empty ref stays empty).
//
// Return value:
//   - nil exit 0 only when the file parses, every phase passes,
//     and every line was created.
//   - ErrUsage (exit 2) when any validation error fired, regardless
//     of --partial-ok. Partial-success creations still commit.
//   - ErrTransient / ErrGeneral for runtime failures during
//     creation — the tx rolls back and no tasks land.
func Batch(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	parsed, err := parseBatchArgs(stderr, args)
	if err != nil {
		return err
	}
	body, rerr := os.ReadFile(parsed.File)
	if rerr != nil {
		return fmt.Errorf("batch: failed to read %s: %s: %w", parsed.File, rerr.Error(), errors.ErrUsage)
	}

	// Phase 1 runs outside the tx — JSON parsing is CPU-only and
	// holding the write lock for it would block other writers.
	lines, phase1Errs := batch.PhaseParse(body)
	if len(lines) == 0 {
		// Either empty_file or every line is malformed JSON. Emit
		// whatever phase 1 found and return exit 2.
		emitBatchErrors(stderr, phase1Errs)
		return fmt.Errorf("batch: %d validation error(s): %w", len(phase1Errs), errors.ErrUsage)
	}

	tx, err := s.BeginImmediate(ctx, store.TxBatchCreate)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	valid := batch.ValidLines(lines, phase1Errs)
	phase2Errs := batch.PhaseReference(ctx, s, lines, valid)
	valid = batch.ValidLines(lines, phase1Errs, phase2Errs)
	phase3Errs := batch.PhaseGraph(ctx, s, lines, valid)
	valid = batch.ValidLines(lines, phase1Errs, phase2Errs, phase3Errs)
	phase4Errs := batch.PhaseSemantic(ctx, s, lines, valid)
	valid = batch.ValidLines(lines, phase1Errs, phase2Errs, phase3Errs, phase4Errs)

	allErrs := append(append(append(phase1Errs, phase2Errs...), phase3Errs...), phase4Errs...)
	emitBatchErrors(stderr, allErrs)

	// Emit a slog WARN record per the "batch validation error"
	// inventory for each error, plus the "batch mode fallthrough"
	// record when --partial-ok routes into a partial creation path.
	for _, e := range allErrs {
		slog.WarnContext(ctx, "batch validation error",
			"phase", e.Phase,
			"code", e.Code,
			"line", e.Line,
		)
	}

	switch {
	case !parsed.PartialOK && len(allErrs) > 0:
		// Atomic mode: any error → no creation, exit 2.
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		telemetry.RecordBatchOutcome(ctx, 0, len(allErrs), "rejected")
		return fmt.Errorf("batch: %d validation error(s): %w", len(allErrs), errors.ErrUsage)
	case parsed.PartialOK && len(allErrs) > 0:
		slog.InfoContext(ctx, "batch mode fallthrough",
			"errors", len(allErrs),
			"valid_lines", countValid(valid),
		)
	}

	pairs, err := batch.Apply(ctx, tx, lines, valid, batch.ApplyOptions{
		IDPrefix:     cfg.Workspace.IDPrefix,
		AgentRole:    cfg.Agent.Role,
		AgentSession: cfg.Agent.Session,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if err := emitPairs(stdout, cfg.Output.Format, pairs); err != nil {
		return err
	}

	outcome := "ok"
	if len(allErrs) > 0 {
		outcome = "partial"
	}
	telemetry.RecordBatchOutcome(ctx, len(pairs), len(allErrs), outcome)

	if len(allErrs) > 0 {
		return fmt.Errorf("batch: %d validation error(s): %w", len(allErrs), errors.ErrUsage)
	}
	return nil
}

// emitBatchErrors writes each BatchError to stderr as one JSONL
// line. Uses the streaming JSONLEncoder because per-code field
// sets are heterogeneous and the slice-typed EmitJSONL form would
// require a common struct with noisy zero-valued fields.
func emitBatchErrors(stderr io.Writer, errs []batch.BatchError) {
	if len(errs) == 0 {
		return
	}
	enc := output.NewJSONLEncoder(stderr)
	for _, e := range errs {
		if err := enc.Encode(e); err != nil {
			// Encoding failure on stderr is a dev-time bug, not a
			// user-facing concern; drop silently rather than loop
			// on a broken writer.
			return
		}
	}
}

// emitPairs writes the ref→id mapping in the active output format.
// JSON mode (default) uses output.EmitJSONL (slice form, uniform
// shape); text mode renders a two-column table via output.Table.
func emitPairs(w io.Writer, format string, pairs []batch.RefIDPair) error {
	if len(pairs) == 0 {
		return nil
	}
	if format == "text" {
		cols := []output.Column{
			{Name: "REF", Width: 24},
			{Name: "ID", Width: 24},
		}
		rows := make([][]string, len(pairs))
		for i, p := range pairs {
			rows[i] = []string{p.Ref, p.ID}
		}
		return output.Table(w, cols, rows)
	}
	return output.EmitJSONL(w, pairs)
}

func countValid(valid map[int]bool) int {
	n := 0
	for _, v := range valid {
		if v {
			n++
		}
	}
	return n
}

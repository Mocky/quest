package command

import (
	"bytes"
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
	fs := newFlagSet("batch", "FILE [--partial-ok]",
		"Create multiple tasks from a JSONL file describing a task graph.")
	fs.SetOutput(stderr)
	partialOK := fs.Bool("partial-ok", false, "create tasks that passed validation even when other lines failed")
	if err := fs.Parse(rest); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return batchArgs{}, err
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
	if stderrors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	body, rerr := os.ReadFile(parsed.File)
	if rerr != nil {
		return fmt.Errorf("batch: failed to read %s: %s: %w", parsed.File, rerr.Error(), errors.ErrUsage)
	}

	// quest.validate parent span wraps every phase so the trace
	// view shows one validate scope with per-phase children
	// (OTEL.md §4.1).
	validateCtx, validateEnd := telemetry.ValidateSpan(ctx)
	defer validateEnd()

	// Phase 1 runs outside the tx — JSON parsing is CPU-only and
	// holding the write lock for it would block other writers.
	parseCtx, parseEnd := telemetry.BatchPhaseSpan(validateCtx, "parse")
	lines, phase1Errs := batch.PhaseParse(body)
	emitBatchPhaseTelemetry(parseCtx, "parse", phase1Errs)
	parseEnd()
	linesTotal, linesBlank := countLines(body, len(lines))
	if len(lines) == 0 {
		// Either empty_file or every line is malformed JSON. Emit
		// whatever phase 1 found and return exit 2.
		emitBatchErrors(stderr, phase1Errs)
		telemetry.RecordBatchOutcome(ctx, linesTotal, linesBlank, parsed.PartialOK, 0, len(phase1Errs))
		return fmt.Errorf("batch: %d validation error(s): %w", len(phase1Errs), errors.ErrUsage)
	}

	tx, err := s.BeginImmediate(ctx, store.TxBatchCreate)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	valid := batch.ValidLines(lines, phase1Errs)
	refCtx, refEnd := telemetry.BatchPhaseSpan(validateCtx, "reference")
	phase2Errs := batch.PhaseReference(ctx, s, lines, valid)
	emitBatchPhaseTelemetry(refCtx, "reference", phase2Errs)
	refEnd()
	valid = batch.ValidLines(lines, phase1Errs, phase2Errs)
	graphCtx, graphEnd := telemetry.BatchPhaseSpan(validateCtx, "graph")
	phase3Errs := batch.PhaseGraph(ctx, s, lines, valid)
	emitBatchPhaseTelemetry(graphCtx, "graph", phase3Errs)
	for _, e := range phase3Errs {
		if e.Code == batch.BatchCodeCycle && len(e.Cycle) > 0 {
			telemetry.RecordCycleDetected(graphCtx, e.Cycle)
		}
	}
	graphEnd()
	valid = batch.ValidLines(lines, phase1Errs, phase2Errs, phase3Errs)
	semCtx, semEnd := telemetry.BatchPhaseSpan(validateCtx, "semantic")
	phase4Errs := batch.PhaseSemantic(ctx, s, lines, valid)
	emitBatchPhaseTelemetry(semCtx, "semantic", phase4Errs)
	semEnd()
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
		telemetry.RecordBatchOutcome(ctx, linesTotal, linesBlank, parsed.PartialOK, 0, len(allErrs))
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

	if err := emitPairs(stdout, cfg.Output.Text, pairs); err != nil {
		return err
	}

	if telemetry.CaptureContentEnabled() {
		for _, line := range lines {
			if !valid[line.LineNo] {
				continue
			}
			if line.Title != "" {
				telemetry.RecordContentTitle(ctx, line.Title)
			}
			if line.Description != "" {
				telemetry.RecordContentDescription(ctx, line.Description)
			}
			if line.Context != "" {
				telemetry.RecordContentContext(ctx, line.Context)
			}
			if line.AcceptanceCriteria != "" {
				telemetry.RecordContentAcceptanceCriteria(ctx, line.AcceptanceCriteria)
			}
		}
	}

	telemetry.RecordBatchOutcome(ctx, linesTotal, linesBlank, parsed.PartialOK, len(pairs), len(allErrs))

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

// emitPairs writes the ref→id mapping in the active output mode.
// JSON mode (default) uses output.EmitJSONL (slice form, uniform
// shape); text mode renders a two-column table via output.Table.
func emitPairs(w io.Writer, text bool, pairs []batch.RefIDPair) error {
	if len(pairs) == 0 {
		return nil
	}
	if text {
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

// emitBatchPhaseTelemetry routes each phase's errors through
// telemetry.RecordBatchError so the active phase span receives a
// quest.batch.error event per failure and dept.quest.batch.errors
// increments by one per failure with phase + code dimensions
// (OTEL.md §8.5).
func emitBatchPhaseTelemetry(ctx context.Context, phase string, errs []batch.BatchError) {
	for _, e := range errs {
		telemetry.RecordBatchError(ctx, phase, e.Code, e.Field, e.Ref, e.Line)
	}
}

// countLines returns the (non-blank, blank) line counts per OTEL.md
// §4.3 — quest.batch.lines_total counts non-blank lines parsed,
// quest.batch.lines_blank counts skipped blanks. parsedNonBlank is
// the count phase 1 actually returned (post empty-trim filter); we
// derive blanks from the file's total line count minus that.
func countLines(body []byte, parsedNonBlank int) (total, blank int) {
	if len(body) == 0 {
		return 0, 0
	}
	totalLines := bytes.Count(body, []byte{'\n'})
	if len(body) > 0 && body[len(body)-1] != '\n' {
		totalLines++
	}
	blank = totalLines - parsedNonBlank
	if blank < 0 {
		blank = 0
	}
	return parsedNonBlank, blank
}

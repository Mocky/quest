package batch

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// PhaseReference is phase 2 of the validator. It checks:
//   - ref uniqueness across the batch (duplicate_ref),
//   - batch-internal ref resolution (unresolved_ref), and
//   - external id resolution against the store (unknown_task_id).
//
// A line whose ref collides with an earlier line's ref produces
// one duplicate_ref error with first_line pointing at the earlier
// line. Its refs are still resolvable downstream — we don't want to
// emit a cascade of unresolved_refs caused by the collision alone.
//
// Lines excluded by valid (failed phase 1) are skipped completely;
// their refs are invisible to phase 2. Per spec "do not fabricate
// derived errors" — a later line whose ref points at a failed line
// must not emit unresolved_ref here.
func PhaseReference(ctx context.Context, s store.Store, lines []BatchLine, valid map[int]bool) []BatchError {
	var errs []BatchError
	// Build two ref maps: `refFirstLine` holds valid-line refs
	// against which forward references must resolve; `anyRef` holds
	// every ref (valid or not) so that a forward reference to a
	// phase-1-failed line is suppressed (spec §Batch validation
	// "quest does not fabricate derived errors"). Duplicate_ref
	// fires only on valid↔valid collisions — a valid line whose
	// ref collides with an already-excluded line is not a duplicate
	// of anything the caller will see in the final graph.
	refFirstLine := map[string]int{}
	anyRef := map[string]bool{}
	for _, line := range lines {
		if line.Ref != "" {
			anyRef[line.Ref] = true
		}
		if !valid[line.LineNo] {
			continue
		}
		if line.Ref == "" {
			continue
		}
		if prev, ok := refFirstLine[line.Ref]; ok {
			errs = append(errs, BatchError{
				Line:      line.LineNo,
				Phase:     PhaseNameReference,
				Code:      BatchCodeDuplicateRef,
				Ref:       line.Ref,
				FirstLine: prev,
				Message:   fmt.Sprintf("ref %q was first defined on line %d", line.Ref, prev),
			})
			continue
		}
		refFirstLine[line.Ref] = line.LineNo
	}
	// Cache of external id lookups so repeat references inside one
	// batch don't hit the DB multiple times.
	idExists := map[string]bool{}
	checkID := func(id string) bool {
		if v, ok := idExists[id]; ok {
			return v
		}
		_, err := s.GetTask(ctx, id)
		exists := err == nil
		if err != nil && !stderrors.Is(err, errors.ErrNotFound) {
			// Any non-not-found error (transient / internal) treat
			// as not-found for validation; the subsequent tx would
			// surface the real failure if the user retries. The
			// duplicate-emitted error here is better than silently
			// skipping.
			exists = false
		}
		idExists[id] = exists
		return exists
	}
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		if line.Parent != nil {
			if line.Parent.Ref != "" {
				if defLine, ok := refFirstLine[line.Parent.Ref]; !ok {
					// Suppress if the ref exists but its defining
					// line was excluded at an earlier phase; the
					// upstream error is the only one worth emitting.
					if !anyRef[line.Parent.Ref] {
						errs = append(errs, BatchError{
							Line:    line.LineNo,
							Phase:   PhaseNameReference,
							Code:    BatchCodeUnresolvedRef,
							Ref:     line.Parent.Ref,
							Message: fmt.Sprintf("parent ref %q does not match any earlier batch line", line.Parent.Ref),
						})
					}
				} else if defLine >= line.LineNo {
					errs = append(errs, BatchError{
						Line:    line.LineNo,
						Phase:   PhaseNameReference,
						Code:    BatchCodeUnresolvedRef,
						Ref:     line.Parent.Ref,
						Message: fmt.Sprintf("parent ref %q must be defined on an earlier line", line.Parent.Ref),
					})
				}
			}
			if line.Parent.ID != "" {
				if !checkID(line.Parent.ID) {
					errs = append(errs, BatchError{
						Line:    line.LineNo,
						Phase:   PhaseNameReference,
						Code:    BatchCodeUnknownTaskID,
						ID:      line.Parent.ID,
						Message: fmt.Sprintf("parent id %q does not exist in the store", line.Parent.ID),
					})
				}
			}
		}
		for _, dep := range line.Dependencies {
			if dep.Target.Ref != "" {
				if defLine, ok := refFirstLine[dep.Target.Ref]; !ok {
					if !anyRef[dep.Target.Ref] {
						errs = append(errs, BatchError{
							Line:    line.LineNo,
							Phase:   PhaseNameReference,
							Code:    BatchCodeUnresolvedRef,
							Ref:     dep.Target.Ref,
							Message: fmt.Sprintf("dependency ref %q does not match any earlier batch line", dep.Target.Ref),
						})
					}
				} else if defLine >= line.LineNo {
					errs = append(errs, BatchError{
						Line:    line.LineNo,
						Phase:   PhaseNameReference,
						Code:    BatchCodeUnresolvedRef,
						Ref:     dep.Target.Ref,
						Message: fmt.Sprintf("dependency ref %q must be defined on an earlier line", dep.Target.Ref),
					})
				}
			}
			if dep.Target.ID != "" {
				if !checkID(dep.Target.ID) {
					errs = append(errs, BatchError{
						Line:    line.LineNo,
						Phase:   PhaseNameReference,
						Code:    BatchCodeUnknownTaskID,
						ID:      dep.Target.ID,
						Message: fmt.Sprintf("dependency id %q does not exist in the store", dep.Target.ID),
					})
				}
			}
		}
	}
	return errs
}

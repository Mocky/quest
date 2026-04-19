package batch

import (
	"context"
	"fmt"

	"github.com/mocky/quest/internal/store"
)

// validLinkTypes is the link-type enum used by phase 4's
// invalid_link_type check. Spec §Relationship Types pins the set.
var validLinkTypes = map[string]bool{
	LinkBlockedBy:      true,
	LinkCausedBy:       true,
	LinkDiscoveredFrom: true,
	LinkRetryOf:        true,
}

// PhaseSemantic is phase 4 of the validator. It runs four checks
// per (valid) line, in the following order so a single malformed
// edge produces the clearest single error:
//   - tag pattern (invalid_tag, field `tags[n]`)
//   - link-type enum (invalid_link_type, field `dependencies[n].type`)
//   - per-line ValidateSemantic against the committed store +
//     same-line edges (blocked_by_cancelled, retry_target_status,
//     source_type_required).
//
// Cycle checks are NOT re-run here — phase 3 owns cross-batch
// cycle detection and phase 4 would double-report. `unknown_task_id`
// errors from ValidateSemantic are suppressed because phase 2 has
// already reported missing targets; suppressing at phase 4 avoids
// the "derived error" cascade the plan rules out.
func PhaseSemantic(ctx context.Context, s store.Store, lines []BatchLine, valid map[int]bool) []BatchError {
	var errs []BatchError
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		errs = append(errs, tagErrors(line)...)
		errs = append(errs, linkTypeErrors(line)...)
		errs = append(errs, semanticDepErrors(ctx, s, line)...)
	}
	return errs
}

// tagErrors iterates line.Tags and emits one invalid_tag per
// failing entry. Every entry is checked independently so a line
// with two bad tags yields two errors, not just the first.
func tagErrors(line BatchLine) []BatchError {
	var errs []BatchError
	for i, t := range line.Tags {
		if _, err := ValidateTag(t); err != nil {
			errs = append(errs, BatchError{
				Line:    line.LineNo,
				Phase:   PhaseNameSemantic,
				Code:    BatchCodeInvalidTag,
				Field:   fmt.Sprintf("tags[%d]", i),
				Value:   t,
				Message: err.Error(),
			})
		}
	}
	return errs
}

// linkTypeErrors checks each dependency entry's Type against the
// enum. Empty Type was already reported as missing_field at phase
// 1; an unrecognized string is the invalid_link_type case.
func linkTypeErrors(line BatchLine) []BatchError {
	var errs []BatchError
	for i, d := range line.Dependencies {
		if d.Type == "" {
			continue
		}
		if validLinkTypes[d.Type] {
			continue
		}
		errs = append(errs, BatchError{
			Line:    line.LineNo,
			Phase:   PhaseNameSemantic,
			Code:    BatchCodeInvalidLinkType,
			Field:   fmt.Sprintf("dependencies[%d].type", i),
			Value:   d.Type,
			Message: fmt.Sprintf("invalid link_type %q (want blocked-by, caused-by, discovered-from, or retry-of)", d.Type),
		})
	}
	return errs
}

// semanticDepErrors invokes ValidateSemantic on the subset of
// dependency entries whose target is an external id. Batch-
// internal refs point at tasks that will be created by this batch
// (status=open at insert time), so running ValidateSemantic on
// them would produce unknown_task_id errors against the committed
// store. Per the plan, phase 4 only checks semantic constraints
// against the pre-existing graph; batch-internal semantic
// correctness for ref-targeted edges is an intentional gap that
// planners manage via their decomposition logic.
func semanticDepErrors(ctx context.Context, s store.Store, line BatchLine) []BatchError {
	var edges []Edge
	for _, d := range line.Dependencies {
		if !validLinkTypes[d.Type] {
			continue
		}
		if d.Target.ID == "" {
			continue
		}
		edges = append(edges, Edge{Target: d.Target.ID, LinkType: d.Type})
	}
	if len(edges) == 0 {
		return nil
	}
	sourceType := line.Type
	if sourceType == "" {
		sourceType = "task"
	}
	depErrs := ValidateSemantic(ctx, s, TaskShape{Type: sourceType}, edges)
	var errs []BatchError
	for _, d := range depErrs {
		switch d.Code {
		case CodeUnknownTaskID:
			// Phase 2 already reported this; suppress the double.
			continue
		case CodeBlockedByCancelled:
			errs = append(errs, BatchError{
				Line:    line.LineNo,
				Phase:   PhaseNameSemantic,
				Code:    BatchCodeBlockedByCancelled,
				Target:  d.Target,
				Message: fmt.Sprintf("blocked-by target %q is cancelled", d.Target),
			})
		case CodeRetryTargetStatus:
			errs = append(errs, BatchError{
				Line:         line.LineNo,
				Phase:        PhaseNameSemantic,
				Code:         BatchCodeRetryTargetStatus,
				Target:       d.Target,
				ActualStatus: d.Detail,
				Message:      fmt.Sprintf("retry-of target %q is %q (must be failed)", d.Target, d.Detail),
			})
		case CodeSourceTypeRequired:
			errs = append(errs, BatchError{
				Line:         line.LineNo,
				Phase:        PhaseNameSemantic,
				Code:         BatchCodeSourceTypeRequired,
				LinkType:     d.Type,
				RequiredType: "bug",
				Message:      fmt.Sprintf("%s requires source type=bug", d.Type),
			})
		}
	}
	return errs
}

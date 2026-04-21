package batch

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/mocky/quest/internal/errors"
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

// PhaseSemantic is phase 4 of the validator. It runs the following
// checks per (valid) line so a single malformed edge produces the
// clearest single error:
//   - tag pattern (invalid_tag, field `tags[n]`)
//   - link-type enum (invalid_link_type, field `dependencies[n].link_type`)
//   - tier enum (invalid_tier, field `tier`) against spec §Model tiers
//   - title byte cap (field_too_long, field `title`) — see spec
//     §Field constraints; mirrors the `--title` cap on `quest create`
//     and `quest update`, expressed in the batch surface as a
//     per-line JSONL semantic-phase error.
//   - external-ID parent status (parent_not_open, field `parent.id`)
//     mirroring `quest create --parent` / `quest move --parent`
//     enforcement — a batch-internal ref parent is always `open` at
//     insert time and is therefore exempt.
//   - per-line ValidateSemantic against the committed store +
//     same-line edges (blocked_by_cancelled, retry_target_status).
//
// Cycle checks are NOT re-run here — phase 3 owns cross-batch
// cycle detection and phase 4 would double-report. `unknown_task_id`
// errors from ValidateSemantic are suppressed because phase 2 has
// already reported missing targets; suppressing at phase 4 avoids
// the "derived error" cascade the plan rules out.
func PhaseSemantic(ctx context.Context, s store.Store, lines []BatchLine, valid map[int]bool) []BatchError {
	var errs []BatchError
	parentStatusCache := map[string]string{}
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		errs = append(errs, tagErrors(line)...)
		errs = append(errs, linkTypeErrors(line)...)
		errs = append(errs, tierEnumErrors(line)...)
		errs = append(errs, severityEnumErrors(line)...)
		errs = append(errs, titleTooLongErrors(line)...)
		errs = append(errs, parentNotOpenErrors(ctx, s, line, parentStatusCache)...)
		errs = append(errs, semanticDepErrors(ctx, s, line)...)
	}
	return errs
}

// titleTooLongErrors enforces the spec §Field constraints 128-byte
// cap on `title`. Empty titles are reported as missing_field at
// phase 1; here we only flag lines whose title exceeds the cap. The
// check counts UTF-8 bytes (len(s)), not code points or graphemes —
// consistent with the `@file` 1 MiB byte-based limit.
func titleTooLongErrors(line BatchLine) []BatchError {
	observed := len(line.Title)
	if observed <= MaxTitleBytes {
		return nil
	}
	return []BatchError{{
		Line:     line.LineNo,
		Phase:    PhaseNameSemantic,
		Code:     BatchCodeFieldTooLong,
		Field:    "title",
		Limit:    MaxTitleBytes,
		Observed: observed,
		Message: fmt.Sprintf("field %q exceeds %d-byte limit (observed %d bytes)",
			"title", MaxTitleBytes, observed),
	}}
}

// tierEnumErrors checks line.Tier against the §Model tiers enum.
// Empty tier is permitted (the spec tier column is nullable); any
// non-empty string outside T0..T6 emits invalid_tier.
func tierEnumErrors(line BatchLine) []BatchError {
	if err := ValidateTier(line.Tier); err != nil {
		return []BatchError{{
			Line:    line.LineNo,
			Phase:   PhaseNameSemantic,
			Code:    BatchCodeInvalidTier,
			Field:   "tier",
			Value:   line.Tier,
			Message: err.Error(),
		}}
	}
	return nil
}

// severityEnumErrors mirrors the tier enum check for the severity
// enum. Empty severity is permitted (nullable column); any non-empty
// string outside the four lowercase enum values emits invalid_severity.
func severityEnumErrors(line BatchLine) []BatchError {
	if err := ValidateSeverity(line.Severity); err != nil {
		return []BatchError{{
			Line:    line.LineNo,
			Phase:   PhaseNameSemantic,
			Code:    BatchCodeInvalidSeverity,
			Field:   "severity",
			Value:   line.Severity,
			Message: err.Error(),
		}}
	}
	return nil
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

// linkTypeErrors checks each dependency entry's LinkType against
// the enum. Empty LinkType was already reported as missing_field at
// phase 1; an unrecognized string is the invalid_link_type case.
func linkTypeErrors(line BatchLine) []BatchError {
	var errs []BatchError
	for i, d := range line.Dependencies {
		if d.LinkType == "" {
			continue
		}
		if validLinkTypes[d.LinkType] {
			continue
		}
		errs = append(errs, BatchError{
			Line:    line.LineNo,
			Phase:   PhaseNameSemantic,
			Code:    BatchCodeInvalidLinkType,
			Field:   fmt.Sprintf("dependencies[%d].link_type", i),
			Value:   d.LinkType,
			Message: fmt.Sprintf("invalid link_type %q (want blocked-by, caused-by, discovered-from, or retry-of)", d.LinkType),
		})
	}
	return errs
}

// parentNotOpenErrors enforces spec §Parent Tasks rule 3 (parent must
// be `open` to accept new children) for lines whose `parent` is an
// external id. Batch-internal ref parents are skipped — a ref points
// at a task this batch is about to create, which is `open` at insert
// time. Phase 2 already confirmed the external id exists, so a
// not-found result here indicates a transient lookup failure; we
// treat it as suppress-and-let-runtime-surface-it, matching the
// phase-2 convention on store errors. The cache shared across lines
// avoids re-fetching when many lines share one parent.
func parentNotOpenErrors(ctx context.Context, s store.Store, line BatchLine, cache map[string]string) []BatchError {
	if line.Parent == nil || line.Parent.ID == "" {
		return nil
	}
	pid := line.Parent.ID
	status, ok := cache[pid]
	if !ok {
		task, err := s.GetTask(ctx, pid)
		if err != nil {
			if stderrors.Is(err, errors.ErrNotFound) {
				cache[pid] = ""
			}
			return nil
		}
		status = task.Status
		cache[pid] = status
	}
	if status == "" || status == "open" {
		return nil
	}
	return []BatchError{{
		Line:         line.LineNo,
		Phase:        PhaseNameSemantic,
		Code:         BatchCodeParentNotOpen,
		Field:        "parent.id",
		ID:           pid,
		ActualStatus: status,
		Message:      fmt.Sprintf("parent %q is not in open status (current: %s)", pid, status),
	}}
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
		if !validLinkTypes[d.LinkType] {
			continue
		}
		if d.Target.ID == "" {
			continue
		}
		edges = append(edges, Edge{Target: d.Target.ID, LinkType: d.LinkType})
	}
	if len(edges) == 0 {
		return nil
	}
	depErrs := ValidateSemantic(ctx, s, TaskShape{}, edges)
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
		}
	}
	return errs
}

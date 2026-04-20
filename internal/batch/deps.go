package batch

import (
	"context"
	stderrors "errors"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// TaskShape describes the source task a set of dependency edges
// originates from. Type drives the per-link-type source-type checks
// (`caused-by` / `discovered-from` require `type=bug`). ID enables
// cycle detection for `blocked-by` — empty ID skips the cycle pass,
// which is the expected state for `quest create` and `quest batch`
// where the source task has not been inserted yet.
type TaskShape struct {
	ID   string
	Type string
}

// Edge is one proposed dependency edge from the source.
type Edge struct {
	Target   string
	LinkType string
}

// SemanticDepError is one rule violation emitted by ValidateSemantic.
// Code is the spec-pinned enum; Target and Type locate the offending
// edge; Detail and Path carry code-specific extras for the batch
// stderr renderer and the stderr message formatter shared by
// `quest create` and `quest link`. Path is populated for `cycle`
// (ordered ID list closing the cycle); Detail carries `actual_status`
// for `retry_target_status` and is empty for other codes.
type SemanticDepError struct {
	Code   string
	Target string
	Type   string
	Detail string
	Path   []string
}

// Error codes emitted by ValidateSemantic. The batch parse / reference
// / graph phases in batch.go own a disjoint code set; `cycle`,
// `blocked_by_cancelled`, `retry_target_status`, `source_type_required`,
// and `unknown_task_id` also appear there but are disambiguated by the
// phase discriminator on batch errors (which is absent on
// SemanticDepError).
const (
	CodeCycle              = "cycle"
	CodeBlockedByCancelled = "blocked_by_cancelled"
	CodeRetryTargetStatus  = "retry_target_status"
	CodeSourceTypeRequired = "source_type_required"
	CodeUnknownTaskID      = "unknown_task_id"
)

// Link type literals. The set mirrors spec §Relationship Types;
// callers (`quest create`, `quest link`, `quest batch`) should use
// these constants rather than raw strings when constructing edges.
const (
	LinkBlockedBy      = "blocked-by"
	LinkCausedBy       = "caused-by"
	LinkDiscoveredFrom = "discovered-from"
	LinkRetryOf        = "retry-of"
)

// ValidateSemantic returns every dependency-rule violation found
// across edges — it does not short-circuit on the first error. The
// caller (create, link, batch) emits each error per its own output
// convention (stderr message for create/link, JSONL for batch).
//
// Scope: this function owns cycle detection (for `blocked-by`) plus
// the per-link-type target-status and source-type checks. Parse and
// reference validation belongs to the batch phases; depth checks live
// with ID generation. Targets that cannot be resolved produce
// `unknown_task_id` and are excluded from subsequent constraint
// checks for that edge.
func ValidateSemantic(ctx context.Context, s store.Store, source TaskShape, edges []Edge) []SemanticDepError {
	var out []SemanticDepError
	cache := map[string]*targetInfo{}

	lookup := func(id string) *targetInfo {
		if info, ok := cache[id]; ok {
			return info
		}
		task, err := s.GetTask(ctx, id)
		if err != nil {
			info := &targetInfo{}
			if stderrors.Is(err, errors.ErrNotFound) {
				info.exists = false
			} else {
				info.lookupErr = err
			}
			cache[id] = info
			return info
		}
		info := &targetInfo{exists: true, status: task.Status, taskType: task.Type}
		cache[id] = info
		return info
	}

	for _, e := range edges {
		info := lookup(e.Target)
		if info.lookupErr != nil || !info.exists {
			out = append(out, SemanticDepError{
				Code:   CodeUnknownTaskID,
				Target: e.Target,
				Type:   e.LinkType,
			})
			continue
		}
		switch e.LinkType {
		case LinkBlockedBy:
			if info.status == "cancelled" {
				out = append(out, SemanticDepError{
					Code:   CodeBlockedByCancelled,
					Target: e.Target,
					Type:   e.LinkType,
				})
			}
		case LinkRetryOf:
			if info.status != "failed" {
				out = append(out, SemanticDepError{
					Code:   CodeRetryTargetStatus,
					Target: e.Target,
					Type:   e.LinkType,
					Detail: info.status,
				})
			}
		case LinkCausedBy, LinkDiscoveredFrom:
			if source.Type != "bug" {
				out = append(out, SemanticDepError{
					Code:   CodeSourceTypeRequired,
					Target: e.Target,
					Type:   e.LinkType,
				})
			}
		}
	}

	if source.ID != "" {
		for _, e := range edges {
			if e.LinkType != LinkBlockedBy {
				continue
			}
			info := lookup(e.Target)
			if info.lookupErr != nil || !info.exists {
				continue
			}
			if path, ok := DetectCycle(ctx, s, source.ID, e.Target, edges); ok {
				out = append(out, SemanticDepError{
					Code:   CodeCycle,
					Target: e.Target,
					Type:   e.LinkType,
					Path:   path,
				})
			}
		}
	}

	return out
}

// DetectCycle reports whether adding a `sourceID --blocked-by
// targetID` edge would create a cycle. It performs iterative DFS from
// targetID over the committed `blocked-by` graph plus the inFlight
// edges (treated as outgoing from sourceID). The returned path starts
// at sourceID, walks the chain targetID → … back to sourceID, and
// ends with targetID again to make the cycle explicit in error
// messages (`A -> B -> C -> A`). cross-cutting.md §Precondition-failed
// events §13.3 caps path lengths at 10 IDs upstream via the telemetry
// truncator — DetectCycle returns the full path; caller truncates.
func DetectCycle(ctx context.Context, s store.Store, sourceID, targetID string, inFlight []Edge) ([]string, bool) {
	if sourceID == targetID {
		return []string{sourceID, targetID}, true
	}
	parent := map[string]string{targetID: ""}
	stack := []string{targetID}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		neighbors := outgoingBlockedBy(ctx, s, n, sourceID, inFlight)
		for _, next := range neighbors {
			if next == sourceID {
				path := []string{sourceID, n}
				for cur := n; cur != targetID; {
					p := parent[cur]
					path = append(path, p)
					cur = p
				}
				path = append(path, sourceID)
				// Reverse the middle so path reads source → target → … → source.
				// The construction above produced [source, n, parent(n), ..., target, source];
				// flip in place to [source, target, ..., parent(n), n, source].
				reverseMiddle(path)
				return path, true
			}
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = n
			stack = append(stack, next)
		}
	}
	return nil, false
}

// outgoingBlockedBy returns the blocked-by targets of n, including
// in-flight edges when n == sourceID. The committed edges come from
// store.GetDependencies; lookup errors are silently skipped because
// DetectCycle has no channel for surfacing them — the caller's
// ValidateSemantic already reported `unknown_task_id` on any missing
// target before cycle detection runs.
func outgoingBlockedBy(ctx context.Context, s store.Store, n, sourceID string, inFlight []Edge) []string {
	var next []string
	if n == sourceID {
		for _, e := range inFlight {
			if e.LinkType == LinkBlockedBy {
				next = append(next, e.Target)
			}
		}
	}
	deps, err := s.GetDependencies(ctx, n)
	if err != nil {
		return next
	}
	for _, d := range deps {
		if d.LinkType == LinkBlockedBy {
			next = append(next, d.ID)
		}
	}
	return next
}

// reverseMiddle flips path[1:len-1] so the constructed sequence reads
// source → ... → source. DetectCycle appends parents in walking
// order; this helper flips that inner region without allocating.
func reverseMiddle(path []string) {
	if len(path) < 4 {
		return
	}
	i, j := 1, len(path)-2
	for i < j {
		path[i], path[j] = path[j], path[i]
		i++
		j--
	}
}

type targetInfo struct {
	exists    bool
	status    string
	taskType  string
	lookupErr error
}

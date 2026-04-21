package batch

import (
	"context"
	"fmt"

	"github.com/mocky/quest/internal/ids"
	"github.com/mocky/quest/internal/store"
)

// PhaseGraph is phase 3 of the validator. Owns two checks:
//   - cycle detection across all proposed blocked-by edges plus the
//     committed blocked-by graph in the store;
//   - depth_exceeded: the parent chain — with any new parent edges
//     introduced by this batch — must keep every new task at depth
//     ≤ 3.
//
// Semantic per-link-type checks are phase 4's job; this phase
// treats every dependency edge whose type string is
// LinkBlockedBy as a graph input. Unknown link types are still
// included here when the string happens to equal "blocked-by";
// invalid type strings yield no graph edge (but get their own
// invalid_link_type error from phase 4).
func PhaseGraph(ctx context.Context, s store.Store, lines []BatchLine, valid map[int]bool) []BatchError {
	var errs []BatchError

	// Build a batch-internal graph: node = batch line ref OR
	// external id; edge = proposed blocked-by. The new-source is
	// identified by its batch ref (lines with empty ref are anonymous
	// and still participate in cycle detection via a synthetic
	// `@line{N}` key so the DFS can reach them).
	nodeKey := func(line BatchLine) string {
		if line.Ref != "" {
			return line.Ref
		}
		return fmt.Sprintf("@line%d", line.LineNo)
	}
	// batchEdges: sourceKey → list of (targetKey, isExternalID)
	batchEdges := map[string][]graphTarget{}
	// refToKey maps a ref to its source line's node key — trivial
	// for refs, but lets the DFS treat refs and anonymous keys
	// uniformly when following batch-internal edges.
	refToKey := map[string]string{}
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		key := nodeKey(line)
		if line.Ref != "" {
			refToKey[line.Ref] = key
		}
	}
	// Populate edges. Use raw Dependencies including invalid link
	// types — those add no edges (phase 4 reports them separately).
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		key := nodeKey(line)
		for _, d := range line.Dependencies {
			if d.LinkType != LinkBlockedBy {
				continue
			}
			var tgtKey string
			external := false
			switch {
			case d.Target.Ref != "":
				tgtKey = d.Target.Ref
			case d.Target.ID != "":
				tgtKey = d.Target.ID
				external = true
			default:
				continue
			}
			batchEdges[key] = append(batchEdges[key], graphTarget{key: tgtKey, externalID: external})
		}
	}

	// Cycle detection per source. DFS from each new source following
	// batch-internal edges (ref targets) and, when hitting an
	// external id, descending into the committed graph via
	// store.GetDependencies. If we hit the original source key, the
	// cycle path is a concrete error.
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		src := nodeKey(line)
		if path, found := batchCycle(ctx, s, src, batchEdges, refToKey); found {
			errs = append(errs, BatchError{
				Line:    line.LineNo,
				Phase:   PhaseNameGraph,
				Code:    BatchCodeCycle,
				Cycle:   path,
				Message: fmt.Sprintf("blocked-by cycle: %s", joinPath(path)),
			})
		}
	}

	// Depth check. For each line with a parent (ref or id), compute
	// the effective depth of the new task:
	//  - parent ref → depth(parent-line's proposed task)+1; the
	//    parent-line's proposed depth is recursively computed from
	//    its own parent ref or id.
	//  - parent id → depth(existing task)+1 via ids.Depth on the id
	//    string (no store read needed — depth is a structural
	//    property of the ID format).
	// Cycles in parent chains are not a graph-cycle case (parents
	// are tree-shaped) but a broken chain is caught by the phase-2
	// unresolved_ref; this phase trusts valid map to have filtered
	// those lines.
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		depth, ok := resolveBatchDepth(line, lines, valid)
		if !ok {
			continue
		}
		if depth > ids.MaxDepth {
			errs = append(errs, BatchError{
				Line:    line.LineNo,
				Phase:   PhaseNameGraph,
				Code:    BatchCodeDepthExceeded,
				Depth:   depth,
				Message: fmt.Sprintf("task would be at depth %d (max %d)", depth, ids.MaxDepth),
			})
		}
	}

	return errs
}

type graphTarget struct {
	key        string
	externalID bool
}

// batchCycle runs iterative DFS from srcKey, following batch-
// internal edges (ref-keyed) and descending into the committed
// graph when reaching an external-id target. Returns the cycle
// path if reaching srcKey again (ordered source → target → ... →
// source).
func batchCycle(ctx context.Context, s store.Store, srcKey string, batchEdges map[string][]graphTarget, refToKey map[string]string) ([]string, bool) {
	parent := map[string]string{srcKey: ""}
	stack := []string{srcKey}
	// Track whether we've stepped away from srcKey at least once;
	// an immediate self-reference via a batch edge to own ref is
	// caught on the first iteration.
	steps := 0
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		steps++
		// Batch-internal edges: follow to their key.
		for _, e := range batchEdges[n] {
			next := e.key
			if e.externalID {
				// External target — descend into committed
				// blocked-by edges from next. We represent external
				// nodes with their raw id so parent lookups stay
				// flat.
				if walkCommittedForCycle(ctx, s, next, srcKey) {
					path := []string{srcKey, n, next, srcKey}
					return path, true
				}
				continue
			}
			if next == srcKey {
				path := reconstructPath(srcKey, n, parent)
				return path, true
			}
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = n
			stack = append(stack, next)
		}
	}
	_ = refToKey
	return nil, false
}

// walkCommittedForCycle walks the committed blocked-by graph from
// startID looking for srcKey. startID is an external id; if any
// blocked-by target transitively leads to srcKey (treated as a
// ref or id match), we report a cycle. srcKey may be either a
// batch ref (invisible to the committed graph) or the raw id of an
// existing task — for refs, the committed graph cannot reach them
// so this traversal returns false; for ids it matches literally.
func walkCommittedForCycle(ctx context.Context, s store.Store, startID, srcKey string) bool {
	visited := map[string]bool{}
	stack := []string{startID}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == srcKey {
			return true
		}
		if visited[n] {
			continue
		}
		visited[n] = true
		deps, err := s.GetDependencies(ctx, n)
		if err != nil {
			continue
		}
		for _, d := range deps {
			if d.LinkType != LinkBlockedBy {
				continue
			}
			stack = append(stack, d.ID)
		}
	}
	return false
}

// reconstructPath walks parent pointers back from n to srcKey and
// returns the forward path [srcKey, target, ..., n, srcKey]. The
// returned slice ends with srcKey twice to make the cycle explicit.
func reconstructPath(srcKey, n string, parent map[string]string) []string {
	path := []string{n}
	for cur := parent[n]; cur != ""; cur = parent[cur] {
		path = append(path, cur)
	}
	// path now reads n → parent(n) → ... → srcKey (excluded since
	// parent[srcKey]="").
	path = append(path, srcKey)
	// Reverse in place so it reads srcKey → ... → n.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	// Close the loop.
	return append(path, srcKey)
}

func joinPath(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " -> "
		}
		out += p
	}
	return out
}

// resolveBatchDepth computes the depth the new task would have
// after insertion. Returns false when the parent chain cannot be
// resolved (unresolved ref / invalid) — phase 2 has already
// reported it. Batch-internal parent chains are walked via the
// parent ref lookups; external-id parents use ids.Depth for the
// structural reading.
func resolveBatchDepth(line BatchLine, lines []BatchLine, valid map[int]bool) (int, bool) {
	const maxChain = 8 // guard against accidental infinite loops
	if line.Parent == nil {
		return 1, true
	}
	refIndex := map[string]BatchLine{}
	for _, l := range lines {
		if valid[l.LineNo] && l.Ref != "" {
			refIndex[l.Ref] = l
		}
	}
	depth := 1
	cur := line.Parent
	for steps := 0; cur != nil && steps < maxChain; steps++ {
		depth++
		switch {
		case cur.ID != "":
			return depth + ids.Depth(cur.ID) - 1, true
		case cur.Ref != "":
			parentLine, ok := refIndex[cur.Ref]
			if !ok {
				return 0, false
			}
			if parentLine.Parent == nil {
				return depth, true
			}
			cur = parentLine.Parent
		default:
			return 0, false
		}
	}
	return 0, false
}

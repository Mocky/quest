package command

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// graphNode is the JSON shape of one entry in the `nodes` array. Field
// order matches spec §quest graph: id, title, status, tier, role,
// severity, children. Tier and role are *string so an unset value emits
// JSON null instead of an empty string. External nodes appear with the
// same shape but children is always []; outgoing edges are not
// expanded.
type graphNode struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Tier     *string  `json:"tier"`
	Role     *string  `json:"role"`
	Severity *string  `json:"severity"`
	Children []string `json:"children"`
}

// graphEdge is one outgoing dependency edge from a subtree task. Field
// names are quest-specific (`task` / `target`) per spec §quest graph
// design notes. LinkType names the relationship primitive (`blocked-by`,
// `caused-by`, …).
type graphEdge struct {
	Task         string `json:"task"`
	LinkType     string `json:"link_type"`
	Target       string `json:"target"`
	TargetStatus string `json:"target_status"`
}

// graphResponse is the top-level JSON envelope: `nodes` then `edges`.
// Both are always non-nil arrays so encoding/json emits [] on the
// empty case (spec §Output & Error Conventions — never omit).
type graphResponse struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// graphFlagSet returns the unparsed FlagSet for `quest graph`. No
// flags — graph takes only the positional ID — but the FlagSet is the
// canonical source of synopsis + description for help rendering.
func graphFlagSet() *flag.FlagSet {
	return newFlagSet("graph", "ID",
		"Display the dependency graph rooted at a task.")
}

// GraphHelp is the descriptor-side help builder.
func GraphHelp() *flag.FlagSet { return graphFlagSet() }

// Graph handles `quest graph ID`. `ID` is required. Traversal descends
// from ID through children and follows dependency edges outward;
// targets outside the subtree appear as unexpanded external nodes per
// spec.
func Graph(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	_ = stdin

	positional, flagArgs := splitLeadingPositional(args)
	fs := graphFlagSet()
	fs.SetOutput(stderr)
	if perr := fs.Parse(flagArgs); perr != nil {
		return fmt.Errorf("graph: %s: %w", perr.Error(), errors.ErrUsage)
	}
	positional = append(positional, fs.Args()...)
	if len(positional) == 0 || positional[0] == "" {
		return fmt.Errorf("graph: quest graph requires an explicit task ID: %w", errors.ErrUsage)
	}
	if len(positional) > 1 {
		return fmt.Errorf("graph: unexpected positional arguments: %w", errors.ErrUsage)
	}
	rootID := positional[0]

	root, err := s.GetTask(ctx, rootID)
	if err != nil {
		return err
	}
	telemetry.RecordTaskContext(ctx, root.ID, root.Tier)

	ctx2, end := telemetry.StoreSpan(ctx, "quest.store.traverse")
	defer func() { end(err) }()

	subtree, childrenByParent, err := collectSubtree(ctx2, s, root)
	if err != nil {
		return err
	}
	subtreeIDs := map[string]struct{}{}
	for _, t := range subtree {
		subtreeIDs[t.ID] = struct{}{}
	}

	var (
		edges       []graphEdge
		externalSet = map[string]struct{}{}
	)
	for _, t := range subtree {
		deps, derr := s.GetDependencies(ctx2, t.ID)
		if derr != nil {
			return derr
		}
		for _, d := range deps {
			edges = append(edges, graphEdge{
				Task:         t.ID,
				LinkType:     d.LinkType,
				Target:       d.ID,
				TargetStatus: d.Status,
			})
			if _, ok := subtreeIDs[d.ID]; !ok {
				externalSet[d.ID] = struct{}{}
			}
		}
	}

	externalIDs := make([]string, 0, len(externalSet))
	for id := range externalSet {
		externalIDs = append(externalIDs, id)
	}
	sort.Strings(externalIDs)

	externals := make([]graphNode, 0, len(externalIDs))
	for _, id := range externalIDs {
		ext, gerr := s.GetTask(ctx2, id)
		if gerr != nil {
			return gerr
		}
		externals = append(externals, graphNode{
			ID:       ext.ID,
			Title:    ext.Title,
			Status:   ext.Status,
			Tier:     nullString(ext.Tier),
			Role:     nullString(ext.Role),
			Severity: nullString(ext.Severity),
			Children: []string{},
		})
	}

	nodes := make([]graphNode, 0, len(subtree)+len(externals))
	for _, t := range subtree {
		cs := childrenByParent[t.ID]
		if cs == nil {
			cs = []string{}
		}
		nodes = append(nodes, graphNode{
			ID:       t.ID,
			Title:    t.Title,
			Status:   t.Status,
			Tier:     nullString(t.Tier),
			Role:     nullString(t.Role),
			Severity: nullString(t.Severity),
			Children: cs,
		})
	}
	nodes = append(nodes, externals...)
	if edges == nil {
		edges = []graphEdge{}
	}

	resp := graphResponse{Nodes: nodes, Edges: edges}
	telemetry.RecordGraphResult(ctx, rootID, len(nodes), len(edges), len(externals), len(subtree))

	if cfg.Output.Text {
		return emitGraphText(stdout, rootID, subtree, externals, edges)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "")
	return enc.Encode(resp)
}

// collectSubtree walks from root through children in BFS order and
// returns the full task list plus a map from parent ID to sorted child
// ID list. Sibling order matches ID-ascending (GetChildren already
// orders by id). Tasks is BFS order so the caller can render the tree
// without a separate pass.
func collectSubtree(ctx context.Context, s store.Store, root store.Task) ([]store.Task, map[string][]string, error) {
	tasks := []store.Task{root}
	seen := map[string]bool{root.ID: true}
	children := map[string][]string{}
	queue := []string{root.ID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		kids, err := s.GetChildren(ctx, parent)
		if err != nil {
			return nil, nil, err
		}
		ids := make([]string, 0, len(kids))
		for _, k := range kids {
			if seen[k.ID] {
				continue
			}
			seen[k.ID] = true
			ids = append(ids, k.ID)
			tasks = append(tasks, k)
			queue = append(queue, k.ID)
		}
		children[parent] = ids
	}
	return tasks, children, nil
}

// emitGraphText renders the indented tree per spec §quest graph
// --text. Every task reference — whether a tree node or an edge
// target — uses the canonical `{id} [{status}] {title}` shape from
// output.TaskRefLine. Parent-child depth is computed from the dotted
// ID offset relative to rootID; externals live outside the subtree
// hierarchy and only surface as edge-target references. The
// titleByID map is built once from the subtree plus externals so each
// edge row looks up target metadata in O(1) without re-querying the
// store.
func emitGraphText(w io.Writer, rootID string, subtree []store.Task, externals []graphNode, edges []graphEdge) error {
	depthOf := func(id string) int {
		if id == rootID {
			return 0
		}
		rest := strings.TrimPrefix(id, rootID+".")
		if rest == id {
			return 0
		}
		return 1 + strings.Count(rest, ".")
	}
	titleByID := map[string]string{}
	statusByID := map[string]string{}
	for _, t := range subtree {
		titleByID[t.ID] = t.Title
		statusByID[t.ID] = t.Status
	}
	for _, n := range externals {
		titleByID[n.ID] = n.Title
		statusByID[n.ID] = n.Status
	}
	edgesBy := map[string][]graphEdge{}
	for _, e := range edges {
		edgesBy[e.Task] = append(edgesBy[e.Task], e)
	}
	var buf bytes.Buffer
	for _, t := range subtree {
		indent := strings.Repeat("  ", depthOf(t.ID))
		fmt.Fprintln(&buf, indent+output.TaskRefLine(t.ID, t.Status, t.Title))
		for _, e := range edgesBy[t.ID] {
			targetRef := output.TaskRefLine(e.Target, statusByID[e.Target], titleByID[e.Target])
			fmt.Fprintf(&buf, "%s  %s  %s\n", indent, e.LinkType, targetRef)
		}
	}
	_, err := w.Write(buf.Bytes())
	return err
}

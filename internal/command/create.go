package command

import (
	"context"
	"database/sql"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/ids"
	"github.com/mocky/quest/internal/input"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// createAck is the spec §Write-command output shapes success body —
// `{"id": "<new-id>"}` is the only field. Agents run `quest show`
// immediately after when they need the full row.
type createAck struct {
	ID string `json:"id"`
}

// createArgs captures every parsed flag. Scalar *string fields use
// the same "nil = unset, non-nil = explicitly provided" pattern as
// updateArgs so the handler can distinguish "default" from "empty
// string passed explicitly" (exit 2) and so the `created` history
// payload only records non-default values. Meta and BlockedBy
// accumulate; the other dep flags track presence via their pointer.
type createArgs struct {
	Title              *string
	Description        *string
	Context            *string
	Parent             *string
	Type               *string
	Tier               *string
	Role               *string
	Tag                *string
	AcceptanceCriteria *string
	Meta               []string
	BlockedBy          []string
	CausedBy           *string
	DiscoveredFrom     *string
	RetryOf            *string
}

// parseCreateArgs consumes every flag listed in spec §Task Creation
// plus validates shape-time preconditions (empty free-form values,
// repeated single-value dep flags, unknown `--type`). @file
// resolution runs through the per-invocation Resolver so errors
// (missing file, >1 MiB, second `@-`) exit 2 before any DB I/O.
func parseCreateArgs(stdin io.Reader, stderr io.Writer, args []string) (createArgs, []string, error) {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var parsed createArgs
	r := input.NewResolver(stdin)

	setText := func(dst **string, flagName string, resolve bool) func(string) error {
		return func(v string) error {
			if resolve {
				resolved, err := r.Resolve(flagName, v)
				if err != nil {
					return err
				}
				v = resolved
			}
			tmp := v
			*dst = &tmp
			return nil
		}
	}

	setSingleID := func(dst **string, flagName string) func(string) error {
		return func(v string) error {
			if *dst != nil {
				return fmt.Errorf("create: %s: repeated; this flag accepts a single value: %w", flagName, errors.ErrUsage)
			}
			tmp := v
			*dst = &tmp
			return nil
		}
	}

	fs.Func("title", "short task title (required)", setText(&parsed.Title, "--title", false))
	fs.Func("description", "full description (supports @file)", setText(&parsed.Description, "--description", true))
	fs.Func("context", "worker context (supports @file)", setText(&parsed.Context, "--context", true))
	fs.Func("parent", "parent task ID", setText(&parsed.Parent, "--parent", false))
	fs.Func("type", "task type: task (default) or bug", setText(&parsed.Type, "--type", false))
	fs.Func("tier", "model tier (T0-T6)", setText(&parsed.Tier, "--tier", false))
	fs.Func("role", "assigned role", setText(&parsed.Role, "--role", false))
	fs.Func("tag", "comma-separated tag list (single flag, not repeatable)", func(v string) error {
		if parsed.Tag != nil {
			return fmt.Errorf("create: --tag: repeated; pass one comma-separated list: %w", errors.ErrUsage)
		}
		tmp := v
		parsed.Tag = &tmp
		return nil
	})
	fs.Func("acceptance-criteria", "verification conditions (supports @file)", setText(&parsed.AcceptanceCriteria, "--acceptance-criteria", true))
	fs.Func("meta", "metadata field KEY=VALUE (repeatable)", func(v string) error {
		parsed.Meta = append(parsed.Meta, v)
		return nil
	})
	fs.Func("blocked-by", "add a blocked-by dependency (repeatable)", func(v string) error {
		parsed.BlockedBy = append(parsed.BlockedBy, v)
		return nil
	})
	fs.Func("caused-by", "add a caused-by link (single value)", setSingleID(&parsed.CausedBy, "--caused-by"))
	fs.Func("discovered-from", "add a discovered-from link (single value)", setSingleID(&parsed.DiscoveredFrom, "--discovered-from"))
	fs.Func("retry-of", "add a retry-of link (single value)", setSingleID(&parsed.RetryOf, "--retry-of"))

	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return createArgs{}, nil, nil
		}
		if stderrors.Is(err, errors.ErrUsage) {
			return createArgs{}, nil, err
		}
		return createArgs{}, nil, fmt.Errorf("create: %s: %w", err.Error(), errors.ErrUsage)
	}
	return parsed, fs.Args(), nil
}

// validateCreateArgs catches the empty-value and shape errors that
// must exit 2 before any DB I/O. `--title` is required and
// non-empty; every other free-form text flag, if provided, must be
// non-empty; every dep ID, if provided, must be non-empty; `--type`,
// if provided, must be one of the spec-enumerated values.
func validateCreateArgs(a createArgs) error {
	if a.Title == nil {
		return fmt.Errorf("create: --title is required: %w", errors.ErrUsage)
	}
	checkNonEmpty := func(name string, v *string) error {
		if v != nil && *v == "" {
			return fmt.Errorf("create: %s: empty value rejected: %w", name, errors.ErrUsage)
		}
		return nil
	}
	if err := checkNonEmpty("--title", a.Title); err != nil {
		return err
	}
	if err := checkNonEmpty("--description", a.Description); err != nil {
		return err
	}
	if err := checkNonEmpty("--context", a.Context); err != nil {
		return err
	}
	if err := checkNonEmpty("--parent", a.Parent); err != nil {
		return err
	}
	if err := checkNonEmpty("--type", a.Type); err != nil {
		return err
	}
	if err := checkNonEmpty("--tier", a.Tier); err != nil {
		return err
	}
	if err := checkNonEmpty("--role", a.Role); err != nil {
		return err
	}
	if err := checkNonEmpty("--acceptance-criteria", a.AcceptanceCriteria); err != nil {
		return err
	}
	if a.Type != nil && *a.Type != "task" && *a.Type != "bug" {
		return fmt.Errorf("create: --type: unknown type %q (want task or bug): %w", *a.Type, errors.ErrUsage)
	}
	// Per-dep-flag non-empty checks. Repeatable --blocked-by
	// validates every entry; single-value dep flags check the one
	// value if present.
	for i, id := range a.BlockedBy {
		if id == "" {
			return fmt.Errorf("create: --blocked-by[%d]: empty value rejected: %w", i, errors.ErrUsage)
		}
	}
	if err := checkNonEmpty("--caused-by", a.CausedBy); err != nil {
		return err
	}
	if err := checkNonEmpty("--discovered-from", a.DiscoveredFrom); err != nil {
		return err
	}
	if err := checkNonEmpty("--retry-of", a.RetryOf); err != nil {
		return err
	}
	// --meta KEY=VALUE shape: reject missing '=', empty key, or
	// empty value — same rule as `quest update --meta`.
	for _, kv := range a.Meta {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("create: --meta %q: missing '=': %w", kv, errors.ErrUsage)
		}
		if key == "" {
			return fmt.Errorf("create: --meta %q: empty key: %w", kv, errors.ErrUsage)
		}
		if value == "" {
			return fmt.Errorf("create: --meta %q: empty value: %w", kv, errors.ErrUsage)
		}
	}
	return nil
}

// buildEdges gathers the dependency flags into the shape
// ValidateSemantic consumes. The single-value dep flags appear
// before --blocked-by so error messages list the edge types in the
// order the user passed them; actual edge ordering does not affect
// validation results.
func buildEdges(a createArgs) []batch.Edge {
	var edges []batch.Edge
	for _, id := range a.BlockedBy {
		edges = append(edges, batch.Edge{Target: id, LinkType: batch.LinkBlockedBy})
	}
	if a.CausedBy != nil {
		edges = append(edges, batch.Edge{Target: *a.CausedBy, LinkType: batch.LinkCausedBy})
	}
	if a.DiscoveredFrom != nil {
		edges = append(edges, batch.Edge{Target: *a.DiscoveredFrom, LinkType: batch.LinkDiscoveredFrom})
	}
	if a.RetryOf != nil {
		edges = append(edges, batch.Edge{Target: *a.RetryOf, LinkType: batch.LinkRetryOf})
	}
	return edges
}

// formatDepErrors turns a list of SemanticDepError into a single
// ErrConflict-wrapped error for the CLI caller. The batch path
// renders these individually to stderr JSONL; create / link render
// one line per call, joining violations with `; ` so an agent can
// parse the full set from the single stderr tail.
func formatDepErrors(errs []batch.SemanticDepError) error {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, formatDepError(e))
	}
	return fmt.Errorf("dependency validation failed: %s: %w",
		strings.Join(parts, "; "), errors.ErrConflict)
}

// formatDepError renders one SemanticDepError for the stderr tail.
// The batch renderer (Task 7.3) maps Code+extras to JSON fields
// directly; this helper is the CLI (non-batch) surface.
func formatDepError(e batch.SemanticDepError) string {
	switch e.Code {
	case batch.CodeCycle:
		return fmt.Sprintf("cycle: %s -> %s (path: %s)", e.Type, e.Target, strings.Join(e.Path, " -> "))
	case batch.CodeBlockedByCancelled:
		return fmt.Sprintf("%s target %s is cancelled", e.Type, e.Target)
	case batch.CodeRetryTargetStatus:
		return fmt.Sprintf("%s target %s is %q (must be failed)", e.Type, e.Target, e.Detail)
	case batch.CodeSourceTypeRequired:
		return fmt.Sprintf("%s requires source type=bug", e.Type)
	case batch.CodeUnknownTaskID:
		return fmt.Sprintf("%s target %s not found", e.Type, e.Target)
	}
	return fmt.Sprintf("%s on %s: %s", e.Code, e.Target, e.Type)
}

// Create inserts a new task. Every invocation runs inside
// s.BeginImmediate(ctx, store.TxCreate) — the counter read/update
// plus task/tag/dep inserts share the write lock so a racing planner
// cannot observe a half-built row. Precondition order matches spec
// §Error precedence: usage (2) pre-tx → parent existence (3) →
// parent status/depth (5) → dep-rule (5) → commit → ack.
func Create(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	parsed, trailing, err := parseCreateArgs(stdin, stderr, args)
	if err != nil {
		return err
	}
	if len(trailing) > 0 {
		return fmt.Errorf("create: unexpected positional arguments: %w", errors.ErrUsage)
	}
	if err := validateCreateArgs(parsed); err != nil {
		return err
	}
	// Pre-tx tag validation: exit 2 before any DB work. The
	// canonical (lowercased, deduped) slice is carried into the tx.
	var tags []string
	if parsed.Tag != nil {
		normalized, tagErr := batch.NormalizeTagList(*parsed.Tag)
		if tagErr != nil {
			return fmt.Errorf("create: --tag: %w", tagErr)
		}
		tags = normalized
	}

	tx, err := s.BeginImmediate(ctx, store.TxCreate)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Parent resolution. `nil` parent = top-level task. An explicit
	// `--parent ID` must resolve to an open task at depth < 3.
	var parentID string
	if parsed.Parent != nil {
		parentID = *parsed.Parent
		if err := checkCreateParent(ctx, tx, parentID); err != nil {
			return err
		}
	}

	// Dependency validation against the committed graph. Source ID
	// is empty because the new row has not been inserted — cycle
	// detection trivially passes since no existing edge can point
	// at a task that does not yet exist.
	edges := buildEdges(parsed)
	if len(edges) > 0 {
		sourceType := "task"
		if parsed.Type != nil {
			sourceType = *parsed.Type
		}
		if depErrs := batch.ValidateSemantic(ctx, s, batch.TaskShape{Type: sourceType}, edges); len(depErrs) > 0 {
			for _, depErr := range depErrs {
				if depErr.Code == batch.CodeCycle {
					telemetry.RecordCycleDetected(ctx, depErr.Path)
				}
			}
			telemetry.RecordPreconditionFailed(ctx, "cycle", nil)
			tx.MarkOutcome(store.TxRolledBackPrecondition)
			return formatDepErrors(depErrs)
		}
	}

	// ID generation. Top-level uses the workspace prefix; sub-task
	// counters live under the parent row. Both share the same
	// tx-scoped INSERT ... ON CONFLICT ... RETURNING pattern so
	// concurrent creates cannot race on the counter.
	var newID string
	if parentID == "" {
		newID, err = ids.NewTopLevel(ctx, tx, cfg.Workspace.IDPrefix)
	} else {
		newID, err = ids.NewSubTask(ctx, tx, parentID)
	}
	if err != nil {
		return err
	}
	if dErr := ids.ValidateDepth(newID); dErr != nil {
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return dErr
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := insertTaskRow(ctx, tx, parsed, parentID, newID, now); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tags(task_id, tag) VALUES (?, ?)`, newID, t); err != nil {
			return fmt.Errorf("%w: create: insert tag: %s", errors.ErrGeneral, err.Error())
		}
	}
	for _, e := range edges {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES (?, ?, ?, ?)`,
			newID, e.Target, e.LinkType, now); err != nil {
			return fmt.Errorf("%w: create: insert dependency: %s", errors.ErrGeneral, err.Error())
		}
	}
	payload := createdHistoryPayload(parsed, tags, edges, parentID)
	if err := store.AppendHistory(ctx, tx, store.History{
		TaskID:    newID,
		Timestamp: now,
		Role:      cfg.Agent.Role,
		Session:   cfg.Agent.Session,
		Action:    store.HistoryCreated,
		Payload:   payload,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	taskType := "task"
	if parsed.Type != nil {
		taskType = *parsed.Type
	}
	tier := ""
	if parsed.Tier != nil {
		tier = *parsed.Tier
	}
	role := ""
	if parsed.Role != nil {
		role = *parsed.Role
	}
	telemetry.RecordTaskCreated(ctx, newID, tier, role, taskType)
	if telemetry.CaptureContentEnabled() {
		if parsed.Title != nil && *parsed.Title != "" {
			telemetry.RecordContentTitle(ctx, *parsed.Title)
		}
		if parsed.Description != nil && *parsed.Description != "" {
			telemetry.RecordContentDescription(ctx, *parsed.Description)
		}
		if parsed.Context != nil && *parsed.Context != "" {
			telemetry.RecordContentContext(ctx, *parsed.Context)
		}
		if parsed.AcceptanceCriteria != nil && *parsed.AcceptanceCriteria != "" {
			telemetry.RecordContentAcceptanceCriteria(ctx, *parsed.AcceptanceCriteria)
		}
	}
	return output.Emit(stdout, cfg.Output.Format, createAck{ID: newID})
}

// checkCreateParent runs the parent-existence, parent-status, and
// depth-limit checks in one query. Existence maps to ErrNotFound
// (exit 3); non-open status + depth maps to ErrConflict (exit 5)
// with a `parent_not_open` / `depth_exceeded` precondition span
// event respectively.
func checkCreateParent(ctx context.Context, tx *store.Tx, parentID string) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, parentID).Scan(&status)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: parent task %q", errors.ErrNotFound, parentID)
		}
		return fmt.Errorf("%w: create: parent lookup: %s", errors.ErrGeneral, err.Error())
	}
	if status != "open" {
		telemetry.RecordPreconditionFailed(ctx, "parent_not_open", []string{parentID})
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("parent %q is not in open status (current: %s): %w", parentID, status, errors.ErrConflict)
	}
	if ids.Depth(parentID)+1 > ids.MaxDepth {
		telemetry.RecordPreconditionFailed(ctx, "depth_exceeded", []string{parentID})
		tx.MarkOutcome(store.TxRolledBackPrecondition)
		return fmt.Errorf("depth exceeded: parent %q is at depth %d (max %d): %w",
			parentID, ids.Depth(parentID), ids.MaxDepth, errors.ErrConflict)
	}
	return nil
}

// insertTaskRow writes the task row with every user-set field. All
// nullable TEXT columns follow cross-cutting.md §Nullable TEXT
// columns: empty Go string → sql.NullString{} → SQL NULL. Metadata
// is always a valid JSON object; the empty case ("{}") is the
// schema default.
func insertTaskRow(ctx context.Context, tx *store.Tx, a createArgs, parentID, id, createdAt string) error {
	description := ""
	if a.Description != nil {
		description = *a.Description
	}
	contextVal := ""
	if a.Context != nil {
		contextVal = *a.Context
	}
	taskType := "task"
	if a.Type != nil {
		taskType = *a.Type
	}
	metadataJSON, err := canonicalMetadata(a.Meta)
	if err != nil {
		return fmt.Errorf("%w: create: %s", errors.ErrGeneral, err.Error())
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO tasks(
			id, title, description, context, type, status,
			role, tier, acceptance_criteria, metadata, parent,
			created_at
		) VALUES (?, ?, ?, ?, ?, 'open',
			?, ?, ?, ?, ?,
			?)`,
		id, *a.Title, description, contextVal, taskType,
		nullableFromPtr(a.Role),
		nullableFromPtr(a.Tier),
		nullableFromPtr(a.AcceptanceCriteria),
		metadataJSON,
		nullableFromString(parentID),
		createdAt,
	)
	if err != nil {
		return fmt.Errorf("%w: create: insert task: %s", errors.ErrGeneral, err.Error())
	}
	return nil
}

// canonicalMetadata builds the canonical JSON from the ordered
// --meta KEY=VALUE slice. Each entry is a string-valued scalar
// (matches `quest update --meta`). Keys are sorted to keep on-disk
// bytes stable across equal value sets.
func canonicalMetadata(meta []string) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	m := map[string]any{}
	for _, kv := range meta {
		key, value, _ := strings.Cut(kv, "=")
		m[key] = value
	}
	return marshalSorted(m)
}

// createdHistoryPayload emits the spec §History field payload for
// `action=created`. Fields at their defaults are omitted per the
// plan ("Fields left at defaults are omitted from the payload, not
// serialized as `null`"). The retrospective queries iterate this
// payload as a `map[string]any`, so unset vs. null vs. omitted is a
// load-bearing distinction.
func createdHistoryPayload(a createArgs, tags []string, edges []batch.Edge, parentID string) map[string]any {
	payload := map[string]any{}
	if a.Tier != nil && *a.Tier != "" {
		payload["tier"] = *a.Tier
	}
	if a.Role != nil && *a.Role != "" {
		payload["role"] = *a.Role
	}
	if a.Type != nil && *a.Type != "task" {
		payload["type"] = *a.Type
	}
	if parentID != "" {
		payload["parent"] = parentID
	}
	if len(tags) > 0 {
		payload["tags"] = tags
	}
	if len(edges) > 0 {
		deps := make([]map[string]any, 0, len(edges))
		for _, e := range edges {
			deps = append(deps, map[string]any{
				"target":    e.Target,
				"link_type": e.LinkType,
			})
		}
		payload["dependencies"] = deps
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

// nullableFromPtr returns sql.NullString{} when p is nil or its
// dereferenced value is empty, matching the nullable-TEXT rule for
// handler-level writes.
func nullableFromPtr(p *string) any {
	if p == nil {
		return sql.NullString{}
	}
	return nullableFromString(*p)
}

// nullableFromString returns sql.NullString{} for "" and the string
// itself otherwise. Shared with insertTaskRow's parent argument and
// with the --role / --tier / --acceptance-criteria writes above.
func nullableFromString(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

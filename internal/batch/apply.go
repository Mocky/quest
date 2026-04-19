package batch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/ids"
	"github.com/mocky/quest/internal/store"
)

// ApplyOptions carries the creation-step inputs the CLI injects:
// workspace prefix for top-level IDs, plus the agent role/session
// used in history entries.
type ApplyOptions struct {
	IDPrefix     string
	AgentRole    string
	AgentSession string
}

// Apply inserts the subset of `lines` marked valid inside tx. It
// processes lines in their original order so a parent ref is
// always inserted before any child that references it, matching
// the spec's "refs resolve backward" rule. A line whose ref /
// parent cannot be resolved to a created-now or existing task is
// silently skipped — phase 2 already reported the reason.
// Returns the ref→id pairs for every row actually inserted, in
// insertion order.
func Apply(ctx context.Context, tx *store.Tx, lines []BatchLine, valid map[int]bool, opts ApplyOptions) ([]RefIDPair, error) {
	refToID := map[string]string{}
	now := time.Now().UTC().Format(time.RFC3339)
	var pairs []RefIDPair
	for _, line := range lines {
		if !valid[line.LineNo] {
			continue
		}
		parentID, ok := resolveParentID(line, refToID)
		if !ok {
			// parent could not be resolved (ref pointed to a failed
			// line); skip silently — phase 2 owns the error report.
			continue
		}
		if !allDepsResolvable(line, refToID) {
			continue
		}
		id, err := createOne(ctx, tx, line, parentID, refToID, now, opts)
		if err != nil {
			return nil, err
		}
		if line.Ref != "" {
			refToID[line.Ref] = id
		}
		pairs = append(pairs, RefIDPair{Ref: line.Ref, ID: id})
	}
	return pairs, nil
}

// resolveParentID maps the line's parent into an actual task id.
// Returns (id, true) for a resolved parent (including the
// top-level "" case for no parent), or (_, false) when the ref
// cannot be resolved to a created-now task.
func resolveParentID(line BatchLine, refToID map[string]string) (string, bool) {
	if line.Parent == nil {
		return "", true
	}
	if line.Parent.ID != "" {
		return line.Parent.ID, true
	}
	id, ok := refToID[line.Parent.Ref]
	return id, ok
}

// allDepsResolvable reports whether every dependency edge on
// `line` can be resolved to a concrete target id given the
// refToID mapping accumulated so far. Any ref without a mapping
// means an upstream line failed and the caller drops this line
// from creation.
func allDepsResolvable(line BatchLine, refToID map[string]string) bool {
	for _, d := range line.Dependencies {
		if !validLinkTypes[d.Type] {
			return false
		}
		if d.Target.ID != "" {
			continue
		}
		if _, ok := refToID[d.Target.Ref]; !ok {
			return false
		}
	}
	return true
}

// createOne inserts one task row plus its tags, dependencies, and
// the `created` history row. Mirrors internal/command/create.go
// closely but driven from BatchLine shape rather than flag pointers
// so edge resolution can walk the ref→id map inline.
func createOne(ctx context.Context, tx *store.Tx, line BatchLine, parentID string, refToID map[string]string, now string, opts ApplyOptions) (string, error) {
	var (
		id  string
		err error
	)
	if parentID == "" {
		id, err = ids.NewTopLevel(ctx, tx, opts.IDPrefix)
	} else {
		id, err = ids.NewSubTask(ctx, tx, parentID)
	}
	if err != nil {
		return "", err
	}
	if dErr := ids.ValidateDepth(id); dErr != nil {
		return "", dErr
	}

	taskType := line.Type
	if taskType == "" {
		taskType = "task"
	}

	// Normalize tags once; phase 4 already reported bad tags, so
	// here every tag passes validation. De-duplication is cheap and
	// guarantees the UNIQUE constraint on (task_id, tag) never fires.
	var tags []string
	if len(line.Tags) > 0 {
		seen := map[string]bool{}
		for _, t := range line.Tags {
			norm, verr := ValidateTag(t)
			if verr != nil {
				// Shouldn't happen after phase 4, but guard anyway
				// — treat as conflict so the tx rolls back.
				return "", fmt.Errorf("%w: batch apply: invalid tag %q", errors.ErrConflict, t)
			}
			if seen[norm] {
				continue
			}
			seen[norm] = true
			tags = append(tags, norm)
		}
	}

	metadataJSON, err := canonicalMetadataJSON(line.Meta)
	if err != nil {
		return "", fmt.Errorf("%w: batch apply: %s", errors.ErrGeneral, err.Error())
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO tasks(
			id, title, description, context, type, status,
			role, tier, acceptance_criteria, metadata, parent,
			created_at
		) VALUES (?, ?, ?, ?, ?, 'open',
			?, ?, ?, ?, ?,
			?)`,
		id, line.Title, line.Description, line.Context, taskType,
		nullable(line.Role),
		nullable(line.Tier),
		nullable(line.AcceptanceCriteria),
		metadataJSON,
		nullable(parentID),
		now,
	)
	if err != nil {
		return "", fmt.Errorf("%w: batch apply: insert task: %s", errors.ErrGeneral, err.Error())
	}
	for _, t := range tags {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tags(task_id, tag) VALUES (?, ?)`, id, t); err != nil {
			return "", fmt.Errorf("%w: batch apply: insert tag: %s", errors.ErrGeneral, err.Error())
		}
	}
	var edgeRecords []map[string]any
	for _, d := range line.Dependencies {
		if !validLinkTypes[d.Type] {
			continue
		}
		target := d.Target.ID
		if target == "" {
			target = refToID[d.Target.Ref]
		}
		if target == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dependencies(task_id, target_id, link_type, created_at) VALUES (?, ?, ?, ?)`,
			id, target, d.Type, now); err != nil {
			return "", fmt.Errorf("%w: batch apply: insert dependency: %s", errors.ErrGeneral, err.Error())
		}
		edgeRecords = append(edgeRecords, map[string]any{
			"target":    target,
			"link_type": d.Type,
		})
	}

	payload := createdPayload(line, parentID, tags, taskType, edgeRecords)
	if err := store.AppendHistory(ctx, tx, store.History{
		TaskID:    id,
		Timestamp: now,
		Role:      opts.AgentRole,
		Session:   opts.AgentSession,
		Action:    store.HistoryCreated,
		Payload:   payload,
	}); err != nil {
		return "", err
	}
	return id, nil
}

// canonicalMetadataJSON renders line.Meta into the canonical
// sort-keyed JSON the tasks.metadata column expects. An empty /
// nil map becomes the schema default `{}`.
func canonicalMetadataJSON(meta map[string]any) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return "", err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(meta[k])
		if err != nil {
			return "", err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.String(), nil
}

// createdPayload mirrors the `created` history payload shape from
// internal/command/create.go: non-default values only, with
// dependencies recorded as ordered {target, link_type} objects.
func createdPayload(line BatchLine, parentID string, tags []string, taskType string, edges []map[string]any) map[string]any {
	payload := map[string]any{}
	if line.Tier != "" {
		payload["tier"] = line.Tier
	}
	if line.Role != "" {
		payload["role"] = line.Role
	}
	if taskType != "" && taskType != "task" {
		payload["type"] = taskType
	}
	if parentID != "" {
		payload["parent"] = parentID
	}
	if len(tags) > 0 {
		payload["tags"] = tags
	}
	if len(edges) > 0 {
		payload["dependencies"] = edges
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

// nullable converts an empty Go string to sql.NullString{} so the
// underlying column writes SQL NULL instead of ”. Mirrors
// store.nullable without crossing the package boundary.
func nullable(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

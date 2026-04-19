package store

import (
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"fmt"

	"github.com/mocky/quest/internal/errors"
)

// selectTaskColumns names the task-row projection in a single place so
// GetTask scans line up with the SELECT. Order matches scanTask's scan
// destinations.
const selectTaskColumns = `id, title, description, context, type, status,
	role, tier, acceptance_criteria, metadata, parent,
	owner_session, started_at, completed_at,
	handoff, handoff_session, handoff_written_at, debrief, created_at`

// scanner is the shared interface implemented by *sql.Row and *sql.Rows
// so scanTask can be called from either the single-row or streaming
// read paths without duplicating the column binding.
type scanner interface {
	Scan(dest ...any) error
}

// scanTask decodes one tasks row into a Task. Nullable TEXT columns
// become empty Go strings per cross-cutting.md §Nullable TEXT columns;
// the rendering layer re-emits them as JSON null via *string. Metadata
// is always a JSON object on disk (NOT NULL DEFAULT '{}') so the
// unmarshal always produces a non-nil map — but guard the nil case so
// accidentally-empty TEXT never leaks as `null` in output.
func scanTask(s scanner) (Task, error) {
	var (
		t                                             Task
		role, tier, acceptCrit, parent                sql.NullString
		ownerSess, startedAt, completedAt             sql.NullString
		handoff, handoffSess, handoffWritten, debrief sql.NullString
		metadataJSON                                  string
	)
	if err := s.Scan(
		&t.ID, &t.Title, &t.Description, &t.Context, &t.Type, &t.Status,
		&role, &tier, &acceptCrit, &metadataJSON, &parent,
		&ownerSess, &startedAt, &completedAt,
		&handoff, &handoffSess, &handoffWritten, &debrief, &t.CreatedAt,
	); err != nil {
		return Task{}, err
	}
	t.Role = role.String
	t.Tier = tier.String
	t.AcceptanceCriteria = acceptCrit.String
	t.Parent = parent.String
	t.OwnerSession = ownerSess.String
	t.StartedAt = startedAt.String
	t.CompletedAt = completedAt.String
	t.Handoff = handoff.String
	t.HandoffSession = handoffSess.String
	t.HandoffWrittenAt = handoffWritten.String
	t.Debrief = debrief.String
	if metadataJSON != "" {
		if err := json.Unmarshal([]byte(metadataJSON), &t.Metadata); err != nil {
			return Task{}, fmt.Errorf("%w: scan metadata: %s", errors.ErrGeneral, err.Error())
		}
	}
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	return t, nil
}

// GetTask reads the task row only. Dependencies, Tags, PRs, and Notes
// are returned as empty slices — callers that need them use
// GetTaskWithDeps (which fans out to the side-table reads). A missing
// row wraps ErrNotFound so the dispatcher maps it to exit 3.
func (s *sqliteStore) GetTask(ctx context.Context, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectTaskColumns+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return Task{}, fmt.Errorf("%w: task %q", errors.ErrNotFound, id)
		}
		return Task{}, classifyDriverErr(err)
	}
	t.Tags = []string{}
	t.Dependencies = []Dependency{}
	t.PRs = []PR{}
	t.Notes = []Note{}
	return t, nil
}

// GetTaskWithDeps reads the task row plus its dependencies (denormalized
// with target title + status), tags, PRs, and notes. Five queries total
// — the dispatcher is not performance-sensitive enough to warrant a
// single wide join, and keeping each read simple makes SQL review easy.
func (s *sqliteStore) GetTaskWithDeps(ctx context.Context, id string) (Task, error) {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	deps, err := s.GetDependencies(ctx, id)
	if err != nil {
		return Task{}, err
	}
	t.Dependencies = deps
	tags, err := s.GetTags(ctx, id)
	if err != nil {
		return Task{}, err
	}
	t.Tags = tags
	prs, err := s.GetPRs(ctx, id)
	if err != nil {
		return Task{}, err
	}
	t.PRs = prs
	notes, err := s.GetNotes(ctx, id)
	if err != nil {
		return Task{}, err
	}
	t.Notes = notes
	return t, nil
}

// ListTasks is a Phase 10 deliverable; leave the not-implemented stub
// until Task 10.2 lands the builder.
func (s *sqliteStore) ListTasks(ctx context.Context, filter Filter) ([]Task, error) {
	_ = ctx
	_ = filter
	return nil, notImplemented("ListTasks")
}

// GetHistory returns the full mutation log for a task in insertion
// order (history.id ASC, matching the AUTOINCREMENT key). Empty payloads
// are decoded as nil maps — show.go's history renderer tolerates nil.
func (s *sqliteStore) GetHistory(ctx context.Context, id string) ([]History, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, timestamp, role, session, action, payload
		 FROM history WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, classifyDriverErr(err)
	}
	defer rows.Close()
	out := []History{}
	for rows.Next() {
		var (
			h           History
			role, sess  sql.NullString
			action      string
			payloadJSON string
		)
		if err := rows.Scan(&h.TaskID, &h.Timestamp, &role, &sess, &action, &payloadJSON); err != nil {
			return nil, classifyDriverErr(err)
		}
		h.Role = role.String
		h.Session = sess.String
		h.Action = HistoryAction(action)
		if payloadJSON != "" && payloadJSON != "{}" {
			if err := json.Unmarshal([]byte(payloadJSON), &h.Payload); err != nil {
				return nil, fmt.Errorf("%w: scan history payload: %s", errors.ErrGeneral, err.Error())
			}
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDriverErr(err)
	}
	return out, nil
}

// GetChildren is used by cancel --recursive and the parent precondition
// checks; wait to implement until Phase 8 so test coverage lands with
// the consuming handler.
func (s *sqliteStore) GetChildren(ctx context.Context, parentID string) ([]Task, error) {
	_ = ctx
	_ = parentID
	return nil, notImplemented("GetChildren")
}

// GetDependencies returns outgoing edges for id (task_id is the source)
// with the target's title and status denormalized via JOIN, per spec
// §quest show. Sorted by (created_at, target_id, link_type) for stable
// output across calls.
func (s *sqliteStore) GetDependencies(ctx context.Context, id string) ([]Dependency, error) {
	const q = `SELECT d.target_id, d.link_type, t.title, t.status
		FROM dependencies d JOIN tasks t ON t.id = d.target_id
		WHERE d.task_id = ?
		ORDER BY d.created_at, d.target_id, d.link_type`
	rows, err := s.db.QueryContext(ctx, q, id)
	if err != nil {
		return nil, classifyDriverErr(err)
	}
	defer rows.Close()
	out := []Dependency{}
	for rows.Next() {
		var d Dependency
		if err := rows.Scan(&d.ID, &d.Type, &d.Title, &d.Status); err != nil {
			return nil, classifyDriverErr(err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDriverErr(err)
	}
	return out, nil
}

// GetDependents returns incoming edges for id (target_id matches).
// Phase 10's `quest deps` will populate title/status via JOIN the same
// way GetDependencies does.
func (s *sqliteStore) GetDependents(ctx context.Context, id string) ([]Dependency, error) {
	_ = ctx
	_ = id
	return nil, notImplemented("GetDependents")
}

// GetTags returns the tag list for id sorted alphabetically per spec
// §quest show ("sorted, lowercase").
func (s *sqliteStore) GetTags(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM tags WHERE task_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, classifyDriverErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, classifyDriverErr(err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDriverErr(err)
	}
	return out, nil
}

// GetPRs returns the PR list ordered by added_at, URL for stable output.
func (s *sqliteStore) GetPRs(ctx context.Context, id string) ([]PR, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT url, added_at FROM prs WHERE task_id = ? ORDER BY added_at, url`, id)
	if err != nil {
		return nil, classifyDriverErr(err)
	}
	defer rows.Close()
	out := []PR{}
	for rows.Next() {
		var p PR
		if err := rows.Scan(&p.URL, &p.AddedAt); err != nil {
			return nil, classifyDriverErr(err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDriverErr(err)
	}
	return out, nil
}

// GetNotes returns notes in insertion order (id ASC) matching the
// timestamp monotonicity the single-writer model guarantees.
func (s *sqliteStore) GetNotes(ctx context.Context, id string) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp, body FROM notes WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, classifyDriverErr(err)
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.Timestamp, &n.Body); err != nil {
			return nil, classifyDriverErr(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDriverErr(err)
	}
	return out, nil
}

// notImplemented is retained for the reads that still live behind
// not-yet-built phases (ListTasks, GetChildren, GetDependents).
// Callers receive a wrapped ErrGeneral so the exit path is a clean
// exit 1 rather than a nil-deref panic.
func notImplemented(method string) error {
	return fmt.Errorf("%w: store.%s not implemented", errors.ErrGeneral, method)
}

package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// TaskJSON is the per-task export shape. Mirrors `quest show --history`
// (spec §`quest export`: "same schema as `quest show --history`
// output"). Contract test TestExportTaskMatchesShowHistory asserts the
// field-by-field parity. Nullable scalars are *string so encoding/json
// emits null (not "") when unset; slices/maps are always non-nil so
// empty values render as [] / {} per spec §Output & Error Conventions.
// History is always present (not omitempty) — export is a full
// archival dump, not a query with a field-presence carve-out.
type TaskJSON struct {
	ID                 string             `json:"id"`
	Title              string             `json:"title"`
	Description        string             `json:"description"`
	Context            string             `json:"context"`
	Type               string             `json:"type"`
	Status             string             `json:"status"`
	Role               *string            `json:"role"`
	Tier               *string            `json:"tier"`
	Tags               []string           `json:"tags"`
	Parent             *string            `json:"parent"`
	AcceptanceCriteria *string            `json:"acceptance_criteria"`
	Metadata           map[string]any     `json:"metadata"`
	OwnerSession       *string            `json:"owner_session"`
	StartedAt          *string            `json:"started_at"`
	CompletedAt        *string            `json:"completed_at"`
	Dependencies       []store.Dependency `json:"dependencies"`
	PRs                []store.PR         `json:"prs"`
	Notes              []store.Note       `json:"notes"`
	Handoff            *string            `json:"handoff"`
	HandoffSession     *string            `json:"handoff_session"`
	HandoffWrittenAt   *string            `json:"handoff_written_at"`
	Debrief            *string            `json:"debrief"`
	History            []HistoryEntry     `json:"history"`
}

// HistoryEntry is one row in TaskJSON.History. Payload keys are
// flattened into the top level via MarshalJSON — the stored payload
// JSON is merged with the canonical timestamp/role/session/action
// fields so consumers see `reason`/`fields`/`content`/... as siblings
// of `action`, matching the shape pinned in quest-spec.md §History
// field. Same shape command/show.go historyEntry produces.
type HistoryEntry struct {
	Timestamp string
	Role      *string
	Session   *string
	Action    string
	Payload   map[string]any
}

// reservedHistoryKeys match show.go's set — canonical top-level keys
// that payload cannot override. Session-actor role wins over payload
// role for retrospective attribution.
var reservedHistoryKeys = map[string]bool{
	"timestamp": true,
	"role":      true,
	"session":   true,
	"action":    true,
}

// MarshalJSON writes timestamp/role/session/action in spec-example
// order followed by payload keys sorted alphabetically — mirrors the
// show.go historyEntry marshaller so exported task files are
// byte-equivalent to `quest show --history` output for the history
// section.
func (h HistoryEntry) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	if err := writeJSONKV(&buf, "timestamp", h.Timestamp, false); err != nil {
		return nil, err
	}
	if err := writeJSONKV(&buf, "role", h.Role, true); err != nil {
		return nil, err
	}
	if err := writeJSONKV(&buf, "session", h.Session, true); err != nil {
		return nil, err
	}
	if err := writeJSONKV(&buf, "action", h.Action, true); err != nil {
		return nil, err
	}
	keys := payloadKeys(h.Payload)
	for _, k := range keys {
		if err := writeJSONKV(&buf, k, h.Payload[k], true); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// HistoryJSONLEntry is one row of history.jsonl. Adds task_id to the
// canonical attributes so the cross-task stream can be consumed
// without a separate index. MarshalJSON pins the key order:
// timestamp, role, session, action, task_id, then payload keys
// alphabetically.
type HistoryJSONLEntry struct {
	Timestamp string
	Role      *string
	Session   *string
	Action    string
	TaskID    string
	Payload   map[string]any
}

// reservedJSONLKeys extends reservedHistoryKeys with task_id — payload
// can't override the structural cross-task key either.
var reservedJSONLKeys = map[string]bool{
	"timestamp": true,
	"role":      true,
	"session":   true,
	"action":    true,
	"task_id":   true,
}

func (h HistoryJSONLEntry) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	if err := writeJSONKV(&buf, "timestamp", h.Timestamp, false); err != nil {
		return nil, err
	}
	if err := writeJSONKV(&buf, "role", h.Role, true); err != nil {
		return nil, err
	}
	if err := writeJSONKV(&buf, "session", h.Session, true); err != nil {
		return nil, err
	}
	if err := writeJSONKV(&buf, "action", h.Action, true); err != nil {
		return nil, err
	}
	if err := writeJSONKV(&buf, "task_id", h.TaskID, true); err != nil {
		return nil, err
	}
	keys := payloadKeysFor(h.Payload, reservedJSONLKeys)
	for _, k := range keys {
		if err := writeJSONKV(&buf, k, h.Payload[k], true); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Summary is the Write return value — the fields Write surfaces to the
// CLI handler for agent-facing confirmation output.
type Summary struct {
	Dir            string
	TaskCount      int
	DebriefCount   int
	HistoryEntries int
}

// Write materializes the quest database at dir. Layout:
//
//	dir/tasks/<id>.json          one file per task (full show --history shape)
//	dir/debriefs/<id>.md         one file per task with a non-empty debrief
//	dir/history.jsonl            chronological event stream across all tasks
//
// Idempotent: re-running overwrites using the track-and-delete-stale
// pattern (phase-11-export.md implementation notes). Every per-file
// write goes through a temp-suffix + os.Rename so a mid-run failure
// never clobbers the previous archive. Stale files for tasks that no
// longer exist are deleted *after* all writes succeed.
func Write(ctx context.Context, s store.Store, dir string) (Summary, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Summary{}, wrap(err)
	}
	tasksDir := filepath.Join(dir, "tasks")
	debriefsDir := filepath.Join(dir, "debriefs")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		return Summary{}, wrap(err)
	}
	if err := os.MkdirAll(debriefsDir, 0o755); err != nil {
		return Summary{}, wrap(err)
	}

	tasks, err := s.ListTasks(ctx, store.Filter{})
	if err != nil {
		return Summary{}, err
	}

	writtenTaskFiles := make(map[string]struct{}, len(tasks))
	writtenDebriefFiles := make(map[string]struct{}, len(tasks))
	debriefCount := 0

	type taskWithHistory struct {
		task    store.Task
		history []store.History
	}
	collected := make([]taskWithHistory, 0, len(tasks))

	for _, summary := range tasks {
		full, err := s.GetTaskWithDeps(ctx, summary.ID)
		if err != nil {
			return Summary{}, err
		}
		history, err := s.GetHistory(ctx, summary.ID)
		if err != nil {
			return Summary{}, err
		}
		collected = append(collected, taskWithHistory{task: full, history: history})

		tj := buildTaskJSON(full, history)
		buf, err := marshalTaskJSON(tj)
		if err != nil {
			return Summary{}, err
		}
		taskFile := filepath.Join(tasksDir, full.ID+".json")
		if err := writeFileAtomic(taskFile, buf); err != nil {
			return Summary{}, wrap(err)
		}
		writtenTaskFiles[full.ID+".json"] = struct{}{}

		if full.Debrief != "" {
			debriefFile := filepath.Join(debriefsDir, full.ID+".md")
			if err := writeFileAtomic(debriefFile, []byte(full.Debrief)); err != nil {
				return Summary{}, wrap(err)
			}
			writtenDebriefFiles[full.ID+".md"] = struct{}{}
			debriefCount++
		}
	}

	entries := make([]HistoryJSONLEntry, 0)
	for _, c := range collected {
		for _, h := range c.history {
			entries = append(entries, HistoryJSONLEntry{
				Timestamp: h.Timestamp,
				Role:      nullString(h.Role),
				Session:   nullString(h.Session),
				Action:    string(h.Action),
				TaskID:    h.TaskID,
				Payload:   h.Payload,
			})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Timestamp != entries[j].Timestamp {
			return entries[i].Timestamp < entries[j].Timestamp
		}
		return entries[i].TaskID < entries[j].TaskID
	})

	var jsonlBuf bytes.Buffer
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return Summary{}, wrap(err)
		}
		jsonlBuf.Write(b)
		jsonlBuf.WriteByte('\n')
	}
	if err := writeFileAtomic(filepath.Join(dir, "history.jsonl"), jsonlBuf.Bytes()); err != nil {
		return Summary{}, wrap(err)
	}

	if err := pruneStale(tasksDir, ".json", writtenTaskFiles); err != nil {
		return Summary{}, wrap(err)
	}
	if err := pruneStale(debriefsDir, ".md", writtenDebriefFiles); err != nil {
		return Summary{}, wrap(err)
	}

	return Summary{
		Dir:            dir,
		TaskCount:      len(tasks),
		DebriefCount:   debriefCount,
		HistoryEntries: len(entries),
	}, nil
}

// buildTaskJSON adapts a store.Task + []store.History into the export
// task shape. Rendering of nullable strings mirrors show.go's rule:
// empty → nil pointer → JSON null.
func buildTaskJSON(t store.Task, history []store.History) TaskJSON {
	hist := make([]HistoryEntry, 0, len(history))
	for _, h := range history {
		hist = append(hist, HistoryEntry{
			Timestamp: h.Timestamp,
			Role:      nullString(h.Role),
			Session:   nullString(h.Session),
			Action:    string(h.Action),
			Payload:   h.Payload,
		})
	}
	tags := t.Tags
	if tags == nil {
		tags = []string{}
	}
	deps := t.Dependencies
	if deps == nil {
		deps = []store.Dependency{}
	}
	prs := t.PRs
	if prs == nil {
		prs = []store.PR{}
	}
	notes := t.Notes
	if notes == nil {
		notes = []store.Note{}
	}
	metadata := t.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return TaskJSON{
		ID:                 t.ID,
		Title:              t.Title,
		Description:        t.Description,
		Context:            t.Context,
		Type:               t.Type,
		Status:             t.Status,
		Role:               nullString(t.Role),
		Tier:               nullString(t.Tier),
		Tags:               tags,
		Parent:             nullString(t.Parent),
		AcceptanceCriteria: nullString(t.AcceptanceCriteria),
		Metadata:           metadata,
		OwnerSession:       nullString(t.OwnerSession),
		StartedAt:          nullString(t.StartedAt),
		CompletedAt:        nullString(t.CompletedAt),
		Dependencies:       deps,
		PRs:                prs,
		Notes:              notes,
		Handoff:            nullString(t.Handoff),
		HandoffSession:     nullString(t.HandoffSession),
		HandoffWrittenAt:   nullString(t.HandoffWrittenAt),
		Debrief:            nullString(t.Debrief),
		History:            hist,
	}
}

// marshalTaskJSON writes the task envelope with the canonical
// showResponse key order preserved (struct definition order) and a
// trailing newline — encoding/json.Encoder.Encode behavior, matched
// here so per-task files are line-terminated and diff-friendly.
func marshalTaskJSON(t TaskJSON) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "")
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeFileAtomic writes data to path via a same-directory temp file
// + os.Rename. `os.Rename` within one filesystem is atomic, so a
// crashed process either leaves the previous content intact or the new
// content fully written — never a partial file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// pruneStale removes files in dir with suffix that are not in keep.
// Called after all writes succeed so a mid-export failure leaves the
// previous archive intact.
func pruneStale(dir, suffix string, keep map[string]struct{}) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		if _, ok := keep[name]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			if _, statErr := fs.Stat(os.DirFS(dir), name); statErr != nil {
				continue
			}
			return err
		}
	}
	return nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

func payloadKeys(payload map[string]any) []string {
	return payloadKeysFor(payload, reservedHistoryKeys)
}

func payloadKeysFor(payload map[string]any, reserved map[string]bool) []string {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		if reserved[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func writeJSONKV(buf *bytes.Buffer, key string, value any, leadingComma bool) error {
	if leadingComma {
		buf.WriteByte(',')
	}
	kb, err := json.Marshal(key)
	if err != nil {
		return err
	}
	buf.Write(kb)
	buf.WriteByte(':')
	vb, err := json.Marshal(value)
	if err != nil {
		return err
	}
	buf.Write(vb)
	return nil
}

func wrap(err error) error {
	return fmt.Errorf("%w: %s", errors.ErrGeneral, err.Error())
}

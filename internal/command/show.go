package command

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/telemetry"
)

// showResponse is the JSON shape emitted by `quest show`. Field order
// matches quest-spec.md §quest show exactly — encoding/json preserves
// struct order, so the struct definition is the schema. Nullable
// scalars are *string so the encoder emits JSON null (not "") when
// unset; slices and the metadata map are always non-nil so they emit
// `[]` / `{}` when empty, per spec §Output & Error Conventions ("never
// omitted"). History is *[]historyEntry with omitempty so the field is
// absent by default and present as `[]` when --history is passed on a
// task with no history — the one documented carve-out in §quest show.
// Parent is a *taskRef so non-root tasks emit the four-field
// `{id,title,status,type}` cluster and root tasks emit JSON null.
type showResponse struct {
	ID                 string             `json:"id"`
	Title              string             `json:"title"`
	Description        string             `json:"description"`
	Context            string             `json:"context"`
	Type               string             `json:"type"`
	Status             string             `json:"status"`
	Role               *string            `json:"role"`
	Tier               *string            `json:"tier"`
	Severity           *string            `json:"severity"`
	Tags               []string           `json:"tags"`
	Parent             *taskRef           `json:"parent"`
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
	History            *[]historyEntry    `json:"history,omitempty"`
}

// taskRef is the four-field task-reference cluster the spec pins as the
// canonical shape wherever a task appears by reference (parent,
// dependency targets, graph edge targets). Field order is the spec
// order — id, title, status, type — and all four keys are always
// present when the wrapping pointer is non-nil.
type taskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Type   string `json:"type"`
}

// historyEntry renders one history row with its action-specific payload
// flattened into the top level. Storage keeps the payload as an opaque
// JSON blob; quest-spec.md §History field shows payload keys (`reason`,
// `content`, `fields`, ...) as siblings of timestamp/role/session/action
// in emitted output.
type historyEntry struct {
	Timestamp string         `json:"timestamp"`
	Role      *string        `json:"role"`
	Session   *string        `json:"session"`
	Action    string         `json:"action"`
	Payload   map[string]any `json:"-"`
}

// reservedHistoryKeys guards the fixed top-level keys against payload
// collisions. Only `created` payloads can carry a colliding `role`
// (the planning role of the created task); the session actor's role at
// the top level is the canonical attribution signal for retrospectives,
// so the top-level wins on collision. Documented in the end-of-phase
// report.
var reservedHistoryKeys = map[string]bool{
	"timestamp": true,
	"role":      true,
	"session":   true,
	"action":    true,
}

// MarshalJSON writes timestamp/role/session/action in spec-example order
// followed by payload keys sorted alphabetically. Go's default map
// marshal sorts keys alphabetically, which would interleave `action`
// and `reason` — doing the write manually keeps the canonical fields
// grouped at the head of each entry.
func (h historyEntry) MarshalJSON() ([]byte, error) {
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
	keys := make([]string, 0, len(h.Payload))
	for k := range h.Payload {
		if reservedHistoryKeys[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := writeJSONKV(&buf, k, h.Payload[k], true); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
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

// Show reads a task and prints its full spec shape. The task ID is a
// required positional argument; omitting it returns ErrUsage so the
// caller gets exit 2 rather than a silent empty result.
func Show(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin

	positional, flagArgs := splitLeadingPositional(args)
	fs := newFlagSet("show")
	fs.SetOutput(stderr)
	historyFlag := fs.Bool("history", false, "include the full mutation history")
	if err := fs.Parse(flagArgs); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("show: %s: %w", err.Error(), errors.ErrUsage)
	}
	// Support ID before or after flags: trailing positionals (after
	// flag.Parse stopped at them) are merged with any leading ID. At
	// most one positional total.
	positional = append(positional, fs.Args()...)
	id, err := resolveWorkerTaskID("show", positional)
	if err != nil {
		return err
	}

	task, err := s.GetTaskWithDeps(ctx, id)
	if err != nil {
		return err
	}
	telemetry.RecordTaskContext(ctx, task.ID, task.Tier, task.Type)

	resp, err := buildShowResponse(ctx, s, task)
	if err != nil {
		return err
	}
	if *historyFlag {
		entries, err := s.GetHistory(ctx, id)
		if err != nil {
			return err
		}
		out := make([]historyEntry, 0, len(entries))
		for _, e := range entries {
			out = append(out, historyEntry{
				Timestamp: e.Timestamp,
				Role:      nullString(e.Role),
				Session:   nullString(e.Session),
				Action:    string(e.Action),
				Payload:   e.Payload,
			})
		}
		resp.History = &out
	}
	if cfg.Output.Text {
		return emitShowText(stdout, resp)
	}
	return output.Emit(stdout, cfg.Output.Text, resp)
}

// buildShowResponse adapts the store.Task (plain-string null-when-empty)
// to the output struct (pointer-string emits-JSON-null-when-nil).
// Slices and the metadata map come through unchanged — the store read
// guarantees non-nil containers. When t.Parent is set the parent row is
// fetched to populate the four-field taskRef cluster; a missing parent
// row (FK integrity says this shouldn't happen) surfaces as an internal
// error rather than silently serializing null.
func buildShowResponse(ctx context.Context, s store.Store, t store.Task) (showResponse, error) {
	var parent *taskRef
	if t.Parent != "" {
		p, err := s.GetTask(ctx, t.Parent)
		if err != nil {
			return showResponse{}, fmt.Errorf("show: load parent %q: %w", t.Parent, err)
		}
		parent = &taskRef{ID: p.ID, Title: p.Title, Status: p.Status, Type: p.Type}
	}
	return showResponse{
		ID:                 t.ID,
		Title:              t.Title,
		Description:        t.Description,
		Context:            t.Context,
		Type:               t.Type,
		Status:             t.Status,
		Role:               nullString(t.Role),
		Tier:               nullString(t.Tier),
		Severity:           nullString(t.Severity),
		Tags:               t.Tags,
		Parent:             parent,
		AcceptanceCriteria: nullString(t.AcceptanceCriteria),
		Metadata:           t.Metadata,
		OwnerSession:       nullString(t.OwnerSession),
		StartedAt:          nullString(t.StartedAt),
		CompletedAt:        nullString(t.CompletedAt),
		Dependencies:       t.Dependencies,
		PRs:                t.PRs,
		Notes:              t.Notes,
		Handoff:            nullString(t.Handoff),
		HandoffSession:     nullString(t.HandoffSession),
		HandoffWrittenAt:   nullString(t.HandoffWrittenAt),
		Debrief:            nullString(t.Debrief),
	}, nil
}

// nullString returns nil for "" so JSON encoding emits null; otherwise
// returns &s so the value is carried through.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

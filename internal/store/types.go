package store

// Task is the row shape returned by GetTask / GetTaskWithDeps /
// ListTasks / GetChildren. JSON tags are the agent-facing contract
// pinned in quest-spec.md §Core fields / §Execution fields —
// TestShowJSONHasRequiredFields (Task 13.1) iterates the tag names
// and asserts them against the spec's required-fields list. created_at
// is an internal ordering column that is not part of the quest show
// JSON contract — json:"-" keeps it out of output.
//
// Scalar fields with a "null-when-unset" contract in the spec
// (owner_session, started_at, completed_at, role, tier,
// acceptance_criteria, handoff, handoff_session, handoff_written_at,
// debrief, parent) are *string / *time-backed through nullable TEXT
// columns: the write path translates empty Go strings to
// sql.NullString{} at each INSERT/UPDATE site, and the read path
// unmarshals them back into empty Go strings. For JSON encoding, a
// pointer-valued field would emit `null` natively; storing strings
// keeps handler code simple and delegates null emission to the
// rendering layer per cross-cutting.md §Nullable TEXT columns.
type Task struct {
	ID                 string         `json:"id"`
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	Context            string         `json:"context"`
	Type               string         `json:"type"`
	Status             string         `json:"status"`
	Role               string         `json:"role"`
	Tier               string         `json:"tier"`
	Tags               []string       `json:"tags"`
	Parent             string         `json:"parent"`
	AcceptanceCriteria string         `json:"acceptance_criteria"`
	Metadata           map[string]any `json:"metadata"`
	OwnerSession       string         `json:"owner_session"`
	StartedAt          string         `json:"started_at"`
	CompletedAt        string         `json:"completed_at"`
	Dependencies       []Dependency   `json:"dependencies"`
	PRs                []PR           `json:"prs"`
	Notes              []Note         `json:"notes"`
	Handoff            string         `json:"handoff"`
	HandoffSession     string         `json:"handoff_session"`
	HandoffWrittenAt   string         `json:"handoff_written_at"`
	Debrief            string         `json:"debrief"`
	CreatedAt          string         `json:"-"`
}

// History is one row of the append-only history table per spec
// §History field. Payload carries action-specific fields
// (reason, fields, content, target, link_type, old_id, new_id, url)
// as an opaque JSON blob — handlers unmarshal per action. Role and
// Session are stored as SQL NULL when unset; AppendHistory is the
// sole call site that performs the empty-string → NULL conversion.
type History struct {
	TaskID    string         `json:"-"`
	Timestamp string         `json:"timestamp"`
	Role      string         `json:"role"`
	Session   string         `json:"session"`
	Action    HistoryAction  `json:"action"`
	Payload   map[string]any `json:"-"`
}

// HistoryAction is the bounded enum of history.action values the
// spec pins in §History field. Typing these at compile time prevents
// handler-side typos. TestHistoryActionEnum (Task 13.1) iterates the
// constants and asserts each matches a spec-listed action.
type HistoryAction string

const (
	HistoryCreated      HistoryAction = "created"
	HistoryAccepted     HistoryAction = "accepted"
	HistoryCompleted    HistoryAction = "completed"
	HistoryFailed       HistoryAction = "failed"
	HistoryCancelled    HistoryAction = "cancelled"
	HistoryReset        HistoryAction = "reset"
	HistoryMoved        HistoryAction = "moved"
	HistoryNoteAdded    HistoryAction = "note_added"
	HistoryPRAdded      HistoryAction = "pr_added"
	HistoryFieldUpdated HistoryAction = "field_updated"
	HistoryLinked       HistoryAction = "linked"
	HistoryUnlinked     HistoryAction = "unlinked"
	HistoryTagged       HistoryAction = "tagged"
	HistoryUntagged     HistoryAction = "untagged"
	HistoryHandoffSet   HistoryAction = "handoff_set"
)

// Dependency is a typed edge between tasks per spec §Multi-type links.
// Title and Status are denormalized from the target task row for
// GetTaskWithDeps — they are populated by the read query's JOIN, not
// persisted in the dependencies table.
type Dependency struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// Note is one row of the notes table. Timestamps are RFC3339 UTC per
// cross-cutting.md §Timestamps.
type Note struct {
	Timestamp string `json:"timestamp"`
	Body      string `json:"body"`
}

// PR is one row of the prs table — an append-only list of URLs
// associated with the task, per spec §Idempotency (duplicates silently
// ignored).
type PR struct {
	URL     string `json:"url"`
	AddedAt string `json:"added_at"`
}

// Filter is the value struct consumed by ListTasks. Every slice field
// is AND-composed at query time — empty slice means "no constraint on
// this dimension". Ready is a shorthand for "status=open AND every
// blocked-by dependency is satisfied" per spec §quest list. Columns
// is a projection hint, not a filter: it controls which auxiliary
// JOINs the SQL builder emits for the tags / blocked-by / children
// columns. Fields not requested remain zero-valued on the returned
// Task.
type Filter struct {
	Statuses []string
	Parents  []string
	Tags     []string
	Roles    []string
	Types    []string
	Tiers    []string
	Ready    bool
	Columns  []string
}

package batch

// BatchLine is one non-blank line from the JSONL input file. When
// phase 1 succeeds for a line every field below reflects the input;
// when phase 1 fails (malformed JSON, missing title, …) the
// orchestrator still keeps a minimal BatchLine with LineNo set so
// the reference phase can see the line's ref (if one was parsed)
// and suppress derived errors on later lines. Fields mirror spec
// §Batch file format plus `quest create` flags; zero values mean
// "not set" — phase 2 / 3 / 4 treat unset fields as defaults.
type BatchLine struct {
	LineNo             int
	Ref                string
	Title              string
	Description        string
	Context            string
	Type               string
	Tier               string
	Role               string
	AcceptanceCriteria string
	Tags               []string
	Meta               map[string]any
	Parent             *RefTarget
	Dependencies       []DepEntry
}

// RefTarget is the spec §Batch file format disambiguated target
// shape: exactly one of Ref or ID is set. Both or neither triggers
// `ambiguous_reference` at phase 1. The parser populates these
// fields with trimmed-but-not-lowercased values; refs are
// case-sensitive because nothing in the spec demands otherwise and
// the export path treats `ref` as an opaque label.
type RefTarget struct {
	Ref string
	ID  string
}

// DepEntry is one parsed dependencies[] entry: a typed edge from
// the line's new task to an earlier batch ref or an existing task.
// Type is validated at phase 4 against the link-type enum; phase 1
// only asserts the field is present as a string.
type DepEntry struct {
	Target RefTarget
	Type   string
}

// BatchError is one error emitted by the four-phase validator. The
// JSONL encoder writes one of these per line to stderr; field
// presence is driven by Code per spec §Batch error output. All
// integer fields use omitempty so the zero value is absent from
// output (intentional for `empty_file` where `line` is omitted; safe
// for `first_line`, `depth`, `limit`, and `observed` because all are
// strictly positive whenever their code emits them).
type BatchError struct {
	Line         int      `json:"line,omitempty"`
	Phase        string   `json:"phase"`
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	Field        string   `json:"field,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	ID           string   `json:"id,omitempty"`
	FirstLine    int      `json:"first_line,omitempty"`
	Cycle        []string `json:"cycle,omitempty"`
	Depth        int      `json:"depth,omitempty"`
	Target       string   `json:"target,omitempty"`
	ActualStatus string   `json:"actual_status,omitempty"`
	LinkType     string   `json:"link_type,omitempty"`
	RequiredType string   `json:"required_type,omitempty"`
	Value        string   `json:"value,omitempty"`
	Limit        int      `json:"limit,omitempty"`
	Observed     int      `json:"observed,omitempty"`
}

// Phase constants used in BatchError.Phase. Spec §Batch error output
// lists exactly these four phases in the order they run.
const (
	PhaseNameParse     = "parse"
	PhaseNameReference = "reference"
	PhaseNameGraph     = "graph"
	PhaseNameSemantic  = "semantic"
)

// Batch error codes. The disjoint parse/reference/graph/semantic
// sets live alongside ValidateSemantic's five codes — five of those
// (cycle, blocked_by_cancelled, retry_target_status,
// source_type_required, unknown_task_id) also appear below with a
// different phase discriminator per cross-cutting §deliberate
// deviations.
const (
	BatchCodeEmptyFile          = "empty_file"
	BatchCodeMalformedJSON      = "malformed_json"
	BatchCodeMissingField       = "missing_field"
	BatchCodeAmbiguousReference = "ambiguous_reference"

	BatchCodeDuplicateRef  = "duplicate_ref"
	BatchCodeUnresolvedRef = "unresolved_ref"
	BatchCodeUnknownTaskID = "unknown_task_id"

	BatchCodeCycle         = "cycle"
	BatchCodeDepthExceeded = "depth_exceeded"

	BatchCodeRetryTargetStatus  = "retry_target_status"
	BatchCodeBlockedByCancelled = "blocked_by_cancelled"
	BatchCodeSourceTypeRequired = "source_type_required"
	BatchCodeInvalidTag         = "invalid_tag"
	BatchCodeInvalidLinkType    = "invalid_link_type"
	BatchCodeInvalidType        = "invalid_type"
	BatchCodeInvalidTier        = "invalid_tier"
	BatchCodeFieldTooLong       = "field_too_long"
	BatchCodeParentNotOpen      = "parent_not_open"
)

// RefIDPair is the stdout JSONL record emitted for each created
// task: `{"ref": "...", "id": "..."}` per spec §Batch output. Lines
// without a `ref` still emit an entry with an empty ref so agents
// can count creations without a second pass.
type RefIDPair struct {
	Ref string `json:"ref"`
	ID  string `json:"id"`
}

package batch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// rawLine is the raw JSON plus the original 1-based line number; the
// parser splits the file into rawLines before phase 1 runs per-line
// validation. Blank lines (whitespace-only) are filtered here but the
// line number remains anchored to the original file so error messages
// cite the on-disk position the planner sees in their editor.
type rawLine struct {
	LineNo int
	Body   []byte
}

// readNonBlankLines splits body by '\n' and returns the non-blank
// lines with their 1-based line numbers preserved. Whitespace-only
// lines are filtered without affecting the indexing so the spec's
// "blank-line spacing for readability" guarantee holds.
func readNonBlankLines(body []byte) []rawLine {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	// Increase the buffer so lines approaching the @file limit (1
	// MiB per free-form flag, and batch lines don't have a separate
	// cap) survive — bufio.Scanner defaults to 64 KiB per token.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []rawLine
	line := 0
	for scanner.Scan() {
		line++
		b := scanner.Bytes()
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		copied := make([]byte, len(b))
		copy(copied, b)
		out = append(out, rawLine{LineNo: line, Body: copied})
	}
	return out
}

// PhaseParse is phase 1 of the validator. For each non-blank line:
// verify the JSON is well-formed, extract the fields we care about,
// assert `title` is present and non-empty, and parse `parent` +
// `dependencies[]` into RefTarget / DepEntry with the
// ambiguous_reference shape check applied to each. Returns the
// parsed lines alongside every error encountered — cross-phase
// isolation (the plan's "valid bitmap") is computed at the
// orchestrator, not inside the phase.
//
// If the input has no non-blank lines, returns exactly one
// `empty_file` error and an empty line slice. Agents parse the
// JSONL stream to detect empty-file failures without needing a
// separate exit-code signal.
func PhaseParse(body []byte) ([]BatchLine, []BatchError) {
	raw := readNonBlankLines(body)
	if len(raw) == 0 {
		return nil, []BatchError{{
			Phase:   PhaseNameParse,
			Code:    BatchCodeEmptyFile,
			Message: "batch file contains no non-blank lines",
		}}
	}

	var (
		lines []BatchLine
		errs  []BatchError
	)
	for _, r := range raw {
		line, lineErrs := parseOneLine(r)
		errs = append(errs, lineErrs...)
		// Always retain the BatchLine (even with errors) so phase 2
		// can see every ref that was at least parse-able. The
		// orchestrator's valid map (built from errs) decides which
		// lines participate in the later phases; reference's
		// derived-error suppression uses the full line set so a
		// forward reference to a phase-1-failed line does not
		// synthesize an unresolved_ref error.
		lines = append(lines, line)
	}
	return lines, errs
}

// parseOneLine decodes one JSON object into a BatchLine. Returns
// the parsed line and zero-or-more phase-1 errors. A line that
// fails JSON parsing is not surfaced further; a line missing a
// required field is still included in the output slice only if
// every required field passes — the orchestrator's validity
// tracking assumes one BatchLine per surviving raw line.
func parseOneLine(r rawLine) (BatchLine, []BatchError) {
	var errs []BatchError
	// Use a strict decoder so stray keys would be caught; we pass
	// DisallowUnknownFields=false intentionally because planners may
	// add forward-compatible fields that quest ignores today. Meta
	// is exposed as a freeform map; new top-level fields would
	// require a spec amendment before being adopted.
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(r.Body, &outer); err != nil {
		errs = append(errs, BatchError{
			Line:    r.LineNo,
			Phase:   PhaseNameParse,
			Code:    BatchCodeMalformedJSON,
			Message: err.Error(),
		})
		return BatchLine{LineNo: r.LineNo}, errs
	}

	line := BatchLine{LineNo: r.LineNo}

	if err := decodeString(outer, "title", &line.Title); err != nil {
		errs = append(errs, batchMissingField(r.LineNo, "title", "required field 'title' is missing"))
	}
	if strings.TrimSpace(line.Title) == "" {
		// Empty / whitespace-only title counts as missing — the
		// `created` history payload has no way to represent an
		// empty title and handlers reject empty strings elsewhere.
		// De-duplicate with the missing-check so each line produces
		// at most one missing_field error for title.
		if !containsMissingField(errs, r.LineNo, "title") {
			errs = append(errs, batchMissingField(r.LineNo, "title", "required field 'title' is missing"))
		}
	}
	_ = decodeString(outer, "ref", &line.Ref)
	_ = decodeString(outer, "description", &line.Description)
	_ = decodeString(outer, "context", &line.Context)
	_ = decodeString(outer, "type", &line.Type)
	_ = decodeString(outer, "tier", &line.Tier)
	_ = decodeString(outer, "role", &line.Role)
	_ = decodeString(outer, "severity", &line.Severity)
	_ = decodeString(outer, "acceptance_criteria", &line.AcceptanceCriteria)

	if raw, ok := outer["tags"]; ok {
		var tags []string
		if err := json.Unmarshal(raw, &tags); err != nil {
			errs = append(errs, BatchError{
				Line:    r.LineNo,
				Phase:   PhaseNameParse,
				Code:    BatchCodeMalformedJSON,
				Message: fmt.Sprintf("field 'tags': %s", err.Error()),
			})
		} else {
			line.Tags = tags
		}
	}

	if raw, ok := outer["metadata"]; ok {
		var meta map[string]any
		if err := json.Unmarshal(raw, &meta); err != nil {
			errs = append(errs, BatchError{
				Line:    r.LineNo,
				Phase:   PhaseNameParse,
				Code:    BatchCodeMalformedJSON,
				Message: fmt.Sprintf("field 'metadata': %s", err.Error()),
			})
		} else {
			line.Meta = meta
		}
	}

	if raw, ok := outer["parent"]; ok {
		ref, perr := parseRefTarget(raw, true)
		if perr != nil {
			errs = append(errs, BatchError{
				Line:    r.LineNo,
				Phase:   PhaseNameParse,
				Code:    BatchCodeAmbiguousReference,
				Field:   "parent",
				Message: perr.Error(),
			})
		} else {
			line.Parent = &ref
		}
	}

	if raw, ok := outer["dependencies"]; ok {
		var depsRaw []json.RawMessage
		if err := json.Unmarshal(raw, &depsRaw); err != nil {
			errs = append(errs, BatchError{
				Line:    r.LineNo,
				Phase:   PhaseNameParse,
				Code:    BatchCodeMalformedJSON,
				Message: fmt.Sprintf("field 'dependencies': %s", err.Error()),
			})
		} else {
			for i, depRaw := range depsRaw {
				dep, depErrs := parseDepEntry(r.LineNo, i, depRaw)
				if len(depErrs) > 0 {
					errs = append(errs, depErrs...)
					continue
				}
				line.Dependencies = append(line.Dependencies, dep)
			}
		}
	}

	return line, errs
}

// parseRefTarget parses either a bare string (legal only when
// allowBareString=true for `parent` field) or an object shape with
// exactly one of `ref` / `id`. Returns a structured error message
// that the caller wraps in a BatchError.
func parseRefTarget(raw json.RawMessage, allowBareString bool) (RefTarget, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return RefTarget{}, fmt.Errorf("value is empty")
	}
	// Bare string: `"foo"` — first non-whitespace byte is '"'.
	if trimmed[0] == '"' {
		if !allowBareString {
			return RefTarget{}, fmt.Errorf("bare string not allowed here; use {\"ref\": ...} or {\"id\": ...}")
		}
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return RefTarget{}, fmt.Errorf("parse bare string: %s", err.Error())
		}
		if s == "" {
			return RefTarget{}, fmt.Errorf("bare string is empty")
		}
		return RefTarget{Ref: s}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return RefTarget{}, fmt.Errorf("expected {\"ref\": ...} or {\"id\": ...}: %s", err.Error())
	}
	var ref, id string
	if raw, ok := obj["ref"]; ok {
		if err := json.Unmarshal(raw, &ref); err != nil {
			return RefTarget{}, fmt.Errorf("field 'ref' must be a string: %s", err.Error())
		}
	}
	if raw, ok := obj["id"]; ok {
		if err := json.Unmarshal(raw, &id); err != nil {
			return RefTarget{}, fmt.Errorf("field 'id' must be a string: %s", err.Error())
		}
	}
	switch {
	case ref != "" && id != "":
		return RefTarget{}, fmt.Errorf("both 'ref' and 'id' are set; use exactly one")
	case ref == "" && id == "":
		return RefTarget{}, fmt.Errorf("one of 'ref' or 'id' is required")
	case ref != "":
		return RefTarget{Ref: ref}, nil
	default:
		return RefTarget{ID: id}, nil
	}
}

// parseDepEntry parses one object from the `dependencies` array.
// Missing `link_type` is reported as missing_field; ambiguous
// reference shape produces ambiguous_reference; errors are
// de-duplicated so a single malformed entry produces at most one
// BatchError per issue class rather than stacking a missing_field on
// top of every ambiguous_reference.
func parseDepEntry(lineNo, idx int, raw json.RawMessage) (DepEntry, []BatchError) {
	fieldPath := fmt.Sprintf("dependencies[%d]", idx)

	target, tErr := parseRefTarget(raw, false)
	var errs []BatchError
	if tErr != nil {
		errs = append(errs, BatchError{
			Line:    lineNo,
			Phase:   PhaseNameParse,
			Code:    BatchCodeAmbiguousReference,
			Field:   fieldPath,
			Message: tErr.Error(),
		})
		// Skip the link_type check — ambiguous reference already
		// locates the problem; piling on missing_field does not
		// help the caller fix the entry.
		return DepEntry{Target: target}, errs
	}

	// Extract `link_type` for the entry — it lives inside the same
	// object as ref/id. Empty link_type is a missing_field error;
	// invalid values are phase 4 (invalid_link_type).
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		if rawT, ok := obj["link_type"]; ok {
			var typ string
			if jerr := json.Unmarshal(rawT, &typ); jerr == nil && typ != "" {
				return DepEntry{Target: target, LinkType: typ}, errs
			}
		}
	}
	errs = append(errs, batchMissingField(lineNo, fieldPath+".link_type",
		"required field '"+fieldPath+".link_type' is missing"))
	return DepEntry{Target: target}, errs
}

// decodeString reads a string field from the outer map. Returns an
// error only when the key is present but not a string; missing keys
// produce no error (the caller checks whether the field is required).
func decodeString(outer map[string]json.RawMessage, key string, dst *string) error {
	raw, ok := outer[key]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func batchMissingField(lineNo int, field, msg string) BatchError {
	return BatchError{
		Line:    lineNo,
		Phase:   PhaseNameParse,
		Code:    BatchCodeMissingField,
		Field:   field,
		Message: msg,
	}
}

func containsMissingField(errs []BatchError, lineNo int, field string) bool {
	for _, e := range errs {
		if e.Line == lineNo && e.Code == BatchCodeMissingField && e.Field == field {
			return true
		}
	}
	return false
}

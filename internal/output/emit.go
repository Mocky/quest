package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Emit writes value to w in the requested format. JSON mode (text
// false) uses a single json.Encoder pass — compact output, one
// trailing newline per quest-spec §Output & Error Conventions. Text
// mode is the human-facing fallback: handlers that render structured
// text (tables, trees) call Table / Tree directly and compose their
// own output; Emit in text mode just `%v`-prints the value so a
// handler that has no text specialization still produces something
// readable on a TTY.
//
// Agents always parse JSON; text mode is not part of the contract per
// STANDARDS.md §CLI Surface Versioning. The pretty-printed examples in
// quest-spec.md are for readability only — real stdout is compact.
func Emit(w io.Writer, text bool, value any) error {
	if text {
		_, err := fmt.Fprintln(w, value)
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "")
	return enc.Encode(value)
}

// EmitJSONL encodes values as one JSON object per line. The bounded
// uniform case covers quest batch's ref→id stdout map (homogeneous
// []RefIDPair). Internally it wraps a single JSONLEncoder so the
// slice and incremental forms cannot drift on quoting, trailing-
// newline, or UTF-8 behavior.
func EmitJSONL[T any](w io.Writer, values []T) error {
	enc := NewJSONLEncoder(w)
	for _, v := range values {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	return nil
}

// JSONLEncoder streams heterogeneous JSON-lines records. Used by the
// batch error stream (different field sets per error code:
// duplicate_ref → first_line; cycle → cycle; etc.). Construct once,
// call Encode per record.
type JSONLEncoder struct {
	enc *json.Encoder
}

// NewJSONLEncoder returns a JSONLEncoder whose Encode method writes
// one JSON object per line to w (compact, newline-terminated per
// json.Encoder's default).
func NewJSONLEncoder(w io.Writer) *JSONLEncoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "")
	return &JSONLEncoder{enc: enc}
}

// Encode writes v as a single JSON line. Repeated calls produce a
// JSONL stream.
func (e *JSONLEncoder) Encode(v any) error {
	return e.enc.Encode(v)
}

package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestEmitJSONNullEmptyContract pins the spec §Output & Error
// Conventions rule: nil pointers emit `null`, empty slices emit `[]`,
// empty maps emit `{}` — never omitted. encoding/json already does
// this natively; the test guards against someone adding an
// `omitempty` tag that would silently violate the contract.
func TestEmitJSONNullEmptyContract(t *testing.T) {
	var nilPtr *string
	type payload struct {
		Note    *string        `json:"note"`
		Tags    []string       `json:"tags"`
		Meta    map[string]any `json:"meta"`
		Present string         `json:"present"`
	}
	v := payload{Note: nilPtr, Tags: []string{}, Meta: map[string]any{}, Present: "hi"}

	var buf bytes.Buffer
	if err := Emit(&buf, "json", v); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	want := `{"note":null,"tags":[],"meta":{},"present":"hi"}`
	if got != want {
		t.Errorf("Emit JSON = %q, want %q", got, want)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("Emit JSON: expected trailing newline")
	}
}

func TestEmitJSONCompact(t *testing.T) {
	var buf bytes.Buffer
	if err := Emit(&buf, "json", map[string]int{"a": 1}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if strings.Contains(buf.String(), "  ") {
		t.Errorf("Emit: expected compact JSON, got %q", buf.String())
	}
}

func TestEmitTextFallback(t *testing.T) {
	var buf bytes.Buffer
	if err := Emit(&buf, "text", "hello"); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if buf.String() != "hello\n" {
		t.Errorf("text Emit = %q, want %q", buf.String(), "hello\n")
	}
}

func TestEmitUnknownFormat(t *testing.T) {
	if err := Emit(&bytes.Buffer{}, "xml", "x"); err == nil {
		t.Fatalf("Emit: expected error for unknown format")
	}
}

// EmitJSONL writes one JSON object per record with a trailing newline
// after each. The batch ref→id stdout stream relies on this exactly.
func TestEmitJSONL(t *testing.T) {
	type pair struct {
		Ref string `json:"ref"`
		ID  string `json:"id"`
	}
	var buf bytes.Buffer
	if err := EmitJSONL(&buf, []pair{
		{Ref: "a", ID: "proj-01"},
		{Ref: "b", ID: "proj-02"},
	}); err != nil {
		t.Fatalf("EmitJSONL: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("EmitJSONL: want 2 lines, got %d: %q", len(lines), buf.String())
	}
	// Each record must round-trip through json.Unmarshal cleanly.
	for i, line := range lines {
		var p pair
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Fatalf("line %d not JSON: %v; raw=%q", i, err, line)
		}
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("EmitJSONL: expected trailing newline")
	}
}

// The incremental JSONLEncoder lets the batch error stream write
// different field sets per record without drifting from EmitJSONL on
// encoding details.
func TestJSONLEncoderHeterogeneous(t *testing.T) {
	var buf bytes.Buffer
	enc := NewJSONLEncoder(&buf)
	if err := enc.Encode(map[string]any{"code": "duplicate_ref", "first_line": 3}); err != nil {
		t.Fatalf("encode 1: %v", err)
	}
	if err := enc.Encode(map[string]any{"code": "cycle", "cycle": []string{"a", "b", "a"}}); err != nil {
		t.Fatalf("encode 2: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
}

// OrderedRow preserves the Columns order on MarshalJSON — required by
// quest list --columns. A plain map[string]any would alphabetize keys.
func TestOrderedRowPreservesOrder(t *testing.T) {
	row := OrderedRow{
		Columns: []string{"id", "status", "title"},
		Values: map[string]any{
			"id":     "proj-01",
			"status": "open",
			"title":  "hello",
		},
	}
	got, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"id":"proj-01","status":"open","title":"hello"}`
	if string(got) != want {
		t.Errorf("OrderedRow = %s, want %s", got, want)
	}
}

// Missing values in OrderedRow emit JSON null — the spec says null is
// never omitted. A column listed in Columns but missing from Values
// still needs to appear in output.
func TestOrderedRowMissingValueEmitsNull(t *testing.T) {
	row := OrderedRow{
		Columns: []string{"id", "status"},
		Values:  map[string]any{"id": "proj-01"},
	}
	got, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"id":"proj-01","status":null}`
	if string(got) != want {
		t.Errorf("OrderedRow = %s, want %s", got, want)
	}
}

// Emitted via Emit in an array, OrderedRow still honors column order.
// Phase 10's quest list builds an []OrderedRow and hands it to Emit.
func TestOrderedRowThroughEmit(t *testing.T) {
	rows := []OrderedRow{
		{Columns: []string{"id", "status"}, Values: map[string]any{"id": "proj-01", "status": "open"}},
		{Columns: []string{"id", "status"}, Values: map[string]any{"id": "proj-02", "status": "accepted"}},
	}
	var buf bytes.Buffer
	if err := Emit(&buf, "json", rows); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	// Verify raw bytes preserve order: first row's "id" precedes "status".
	if !strings.Contains(buf.String(), `"id":"proj-01","status":"open"`) {
		t.Errorf("order lost in JSON: %s", buf.String())
	}
}

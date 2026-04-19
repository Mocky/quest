package testutil

import (
	"bytes"
	"encoding/json"
	"testing"
)

// AssertSchema unmarshals got as a JSON object and fails t if any key
// named in required is missing. Used by Phase 6+ contract tests that
// assert a command's JSON output always carries the spec-pinned
// fields — emitting `null` / `[]` / `{}` is enough (spec §Output &
// Error Conventions: "never omitted"), but the key must be present.
// Does not validate value shapes; pair with json.Unmarshal into the
// command's struct type for shape checks.
func AssertSchema(t *testing.T, got []byte, required []string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("AssertSchema: not a JSON object: %v; raw=%s", err, got)
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Errorf("AssertSchema: missing required key %q in %s", k, got)
		}
	}
}

// AssertJSONKeyOrder decodes got as a JSON object and verifies the
// top-level keys appear in the order want lists them. It catches
// regressions where a refactor (e.g., switching an output struct to a
// plain `map[string]any`) silently sorts keys alphabetically. Uses
// json.Decoder so the original byte order is preserved — Unmarshal
// into a map drops it. Extra keys not in want are allowed; want is
// the contract anchor (spec-pinned key sequence) and must appear in
// that relative order.
func AssertJSONKeyOrder(t *testing.T, got []byte, want []string) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(got))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("AssertJSONKeyOrder: %v; raw=%s", err, got)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("AssertJSONKeyOrder: top-level not object; raw=%s", got)
	}
	keys := []string{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatalf("AssertJSONKeyOrder: key read: %v; raw=%s", err, got)
		}
		ks, ok := k.(string)
		if !ok {
			t.Fatalf("AssertJSONKeyOrder: non-string key %v", k)
		}
		keys = append(keys, ks)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("AssertJSONKeyOrder: value read: %v", err)
		}
	}
	idx := 0
	for _, k := range keys {
		if idx >= len(want) {
			break
		}
		if k == want[idx] {
			idx++
		}
	}
	if idx != len(want) {
		t.Errorf("AssertJSONKeyOrder: want order %v; got order %v", want, keys)
	}
}

package testutil

import (
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

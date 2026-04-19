//go:build integration

package batch_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mocky/quest/internal/batch"
)

// TestBatchOutputShape pins the §quest batch stdout shape: every
// created task emits a JSONL line `{"ref": "...", "id": "..."}` with
// both fields present even when ref is empty (lines without a ref get
// "" so callers can count creations without a second pass).
//
// Companion to TestBatchStderrShape (which lives in batch_test.go and
// covers the per-code stderr field set). Together they pin the full
// `quest batch` wire contract.
func TestBatchOutputShape(t *testing.T) {
	pairs := []batch.RefIDPair{
		{Ref: "alpha", ID: "proj-a1"},
		{Ref: "", ID: "proj-a2"}, // unrefd line still emits the pair
	}
	for _, p := range pairs {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal %+v: %v", p, err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, f := range []string{"ref", "id"} {
			if _, ok := raw[f]; !ok {
				t.Errorf("pair %+v: missing field %q in %s", p, f, b)
			}
		}
		if string(raw["id"]) == `""` {
			t.Errorf("pair %+v: id is empty string in output", p)
		}
	}
}

// TestBatchOutputShapeOrder pins ref-then-id key order in the JSONL
// output. encoding/json on the RefIDPair struct preserves declaration
// order today; a refactor to a map would silently sort keys
// alphabetically — assert the on-the-wire byte order so a future
// regression fails the contract test.
func TestBatchOutputShapeOrder(t *testing.T) {
	b, err := json.Marshal(batch.RefIDPair{Ref: "alpha", ID: "proj-a1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.HasPrefix(b, []byte(`{"ref":`)) {
		t.Errorf("output should start with {\"ref\":...; got %s", b)
	}
	if !strings.Contains(string(b), `"ref":"alpha","id":"proj-a1"`) {
		t.Errorf("expected ref-then-id key order; got %s", b)
	}
}

// TestBatchInvalidLinkTypeCodeRecognized pins the invalid_link_type
// code added in the cross-cutting deviations as a phase-4 (semantic)
// emission. The code constant must exist and be the literal string
// callers parse from the stderr JSONL.
func TestBatchInvalidLinkTypeCodeRecognized(t *testing.T) {
	if batch.BatchCodeInvalidLinkType != "invalid_link_type" {
		t.Errorf("BatchCodeInvalidLinkType = %q, want %q",
			batch.BatchCodeInvalidLinkType, "invalid_link_type")
	}
	if batch.BatchCodeInvalidTag != "invalid_tag" {
		t.Errorf("BatchCodeInvalidTag = %q, want %q",
			batch.BatchCodeInvalidTag, "invalid_tag")
	}
}

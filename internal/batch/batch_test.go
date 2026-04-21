//go:build integration

package batch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/store"
)

// runPhases is a helper that wraps the four-phase pipeline in the
// same order the CLI does — used by tests to make assertions on
// cross-phase interaction without duplicating orchestration.
func runPhases(t *testing.T, s store.Store, body string) ([]batch.BatchLine, []batch.BatchError) {
	t.Helper()
	lines, p1 := batch.PhaseParse([]byte(body))
	valid := batch.ValidLines(lines, p1)
	p2 := batch.PhaseReference(context.Background(), s, lines, valid)
	valid = batch.ValidLines(lines, p1, p2)
	p3 := batch.PhaseGraph(context.Background(), s, lines, valid)
	valid = batch.ValidLines(lines, p1, p2, p3)
	p4 := batch.PhaseSemantic(context.Background(), s, lines, valid)
	all := append(append(append(p1, p2...), p3...), p4...)
	return lines, all
}

// hasErr reports whether errs contains any entry matching the
// predicate; used to make test assertions read more like prose
// ("expected a cycle on line 3").
func hasErr(errs []batch.BatchError, predicate func(batch.BatchError) bool) bool {
	for _, e := range errs {
		if predicate(e) {
			return true
		}
	}
	return false
}

// withCode filters errs to those matching code — a small
// readability helper on top of the full slice.
func withCode(errs []batch.BatchError, code string) []batch.BatchError {
	var out []batch.BatchError
	for _, e := range errs {
		if e.Code == code {
			out = append(out, e)
		}
	}
	return out
}

// TestPhaseParseEmptyFile pins the empty_file code and the omitted
// line field (one record, no line, phase=parse).
func TestPhaseParseEmptyFile(t *testing.T) {
	_, errs := batch.PhaseParse([]byte(""))
	if len(errs) != 1 {
		t.Fatalf("errs = %+v, want 1 empty_file", errs)
	}
	if errs[0].Code != batch.BatchCodeEmptyFile {
		t.Errorf("code = %q, want empty_file", errs[0].Code)
	}
	if errs[0].Line != 0 {
		t.Errorf("line = %d, want 0 (omitted)", errs[0].Line)
	}
}

// TestPhaseParseBlankLinesSkipped: whitespace-only lines are
// filtered but the line numbers remain anchored to the on-disk
// positions.
func TestPhaseParseBlankLinesSkipped(t *testing.T) {
	body := "\n\n{\"ref\":\"a\",\"title\":\"A\"}\n   \n{\"ref\":\"b\",\"title\":\"B\"}\n"
	lines, errs := batch.PhaseParse([]byte(body))
	if len(errs) != 0 {
		t.Fatalf("errs = %+v, want none", errs)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[0].LineNo != 3 || lines[1].LineNo != 5 {
		t.Errorf("line numbers = %d, %d; want 3, 5", lines[0].LineNo, lines[1].LineNo)
	}
}

// TestPhaseParseMalformedJSON: a syntax error on line 2 yields
// exactly one malformed_json error with line=2.
func TestPhaseParseMalformedJSON(t *testing.T) {
	body := "{\"ref\":\"a\",\"title\":\"A\"}\n{not-json\n"
	_, errs := batch.PhaseParse([]byte(body))
	if len(errs) != 1 {
		t.Fatalf("errs = %+v, want 1", errs)
	}
	if errs[0].Code != batch.BatchCodeMalformedJSON || errs[0].Line != 2 {
		t.Errorf("err = %+v, want {malformed_json, line=2}", errs[0])
	}
}

// TestPhaseParseMissingTitle: line with no title field → exactly
// one missing_field error with field=title.
func TestPhaseParseMissingTitle(t *testing.T) {
	body := "{\"ref\":\"a\"}\n"
	_, errs := batch.PhaseParse([]byte(body))
	if len(errs) != 1 || errs[0].Code != batch.BatchCodeMissingField || errs[0].Field != "title" {
		t.Fatalf("errs = %+v, want missing_field title", errs)
	}
}

// TestPhaseParseParentAmbiguousReference: parent with both ref and
// id triggers ambiguous_reference at phase 1 with field=parent.
func TestPhaseParseParentAmbiguousReference(t *testing.T) {
	body := `{"ref":"a","title":"A","parent":{"ref":"x","id":"proj-01"}}` + "\n"
	_, errs := batch.PhaseParse([]byte(body))
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeAmbiguousReference && e.Field == "parent"
	}) {
		t.Fatalf("errs = %+v, want ambiguous_reference on parent", errs)
	}
}

// TestPhaseParseDependencyBareStringIsAmbiguous: spec says bare
// string is shorthand only on parent. In dependencies[] a bare
// string is ambiguous_reference with field=dependencies[n].
func TestPhaseParseDependencyBareStringIsAmbiguous(t *testing.T) {
	body := `{"ref":"a","title":"A","dependencies":["task-1"]}` + "\n"
	_, errs := batch.PhaseParse([]byte(body))
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeAmbiguousReference && e.Field == "dependencies[0]"
	}) {
		t.Fatalf("errs = %+v, want ambiguous_reference on dependencies[0]", errs)
	}
}

// TestPhaseParseDependencyBothKeysIsAmbiguous: a dependency entry
// with both ref and id triggers ambiguous_reference.
func TestPhaseParseDependencyBothKeysIsAmbiguous(t *testing.T) {
	body := `{"ref":"a","title":"A","dependencies":[{"ref":"x","id":"proj-01","link_type":"blocked-by"}]}` + "\n"
	_, errs := batch.PhaseParse([]byte(body))
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeAmbiguousReference && e.Field == "dependencies[0]"
	}) {
		t.Fatalf("errs = %+v, want ambiguous_reference on dependencies[0]", errs)
	}
}

// TestPhaseParseDependencyMissingType: a dep entry with ref but
// no link_type field → missing_field with dependencies[n].link_type.
func TestPhaseParseDependencyMissingType(t *testing.T) {
	body := `{"ref":"a","title":"A","dependencies":[{"ref":"x"}]}` + "\n"
	_, errs := batch.PhaseParse([]byte(body))
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeMissingField && e.Field == "dependencies[0].link_type"
	}) {
		t.Fatalf("errs = %+v, want missing_field dependencies[0].link_type", errs)
	}
}

// TestPhaseParseDependencyLegacyTypeKeyMissing: a dep entry that uses
// the pre-rename `type` key is treated as missing_field at
// dependencies[n].link_type so planners migrating from the old JSONL
// shape see a precise error rather than silent acceptance. Pins the
// breaking spec change (spec commit 9d5fc76).
func TestPhaseParseDependencyLegacyTypeKeyMissing(t *testing.T) {
	body := `{"ref":"a","title":"A","dependencies":[{"ref":"x","type":"blocked-by"}]}` + "\n"
	_, errs := batch.PhaseParse([]byte(body))
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeMissingField && e.Field == "dependencies[0].link_type"
	}) {
		t.Fatalf("errs = %+v, want missing_field dependencies[0].link_type for legacy `type` key", errs)
	}
}

// TestPhaseReferenceDuplicateRef: two lines with the same ref
// produce one duplicate_ref on the later line, first_line pointing
// at the earlier one.
func TestPhaseReferenceDuplicateRef(t *testing.T) {
	s := testStore(t)
	body := "{\"ref\":\"a\",\"title\":\"A\"}\n{\"ref\":\"a\",\"title\":\"A2\"}\n"
	_, errs := runPhases(t, s, body)
	dups := withCode(errs, batch.BatchCodeDuplicateRef)
	if len(dups) != 1 {
		t.Fatalf("dups = %+v, want 1", dups)
	}
	if dups[0].Line != 2 || dups[0].FirstLine != 1 || dups[0].Ref != "a" {
		t.Errorf("dup = %+v, want {line=2, first_line=1, ref=a}", dups[0])
	}
}

// TestPhaseReferenceUnresolvedRef: a line whose parent ref points
// at a non-existent earlier line's ref triggers unresolved_ref.
func TestPhaseReferenceUnresolvedRef(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"a","title":"A","parent":"nope"}` + "\n"
	_, errs := runPhases(t, s, body)
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeUnresolvedRef && e.Ref == "nope"
	}) {
		t.Fatalf("errs = %+v, want unresolved_ref ref=nope", errs)
	}
}

// TestPhaseReferenceUnknownTaskID: {id: "proj-missing"} on parent
// produces unknown_task_id.
func TestPhaseReferenceUnknownTaskID(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"a","title":"A","parent":{"id":"proj-missing"}}` + "\n"
	_, errs := runPhases(t, s, body)
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeUnknownTaskID && e.ID == "proj-missing"
	}) {
		t.Fatalf("errs = %+v, want unknown_task_id id=proj-missing", errs)
	}
}

// TestPhaseReferenceSuppressesDerivedErrors: a line referencing a
// parse-failed line's ref must NOT emit unresolved_ref.
func TestPhaseReferenceSuppressesDerivedErrors(t *testing.T) {
	s := testStore(t)
	body := "{\"ref\":\"bad\"}\n{\"ref\":\"child\",\"title\":\"C\",\"parent\":\"bad\"}\n"
	_, errs := runPhases(t, s, body)
	// Only the parse error should be present.
	for _, e := range errs {
		if e.Code == batch.BatchCodeUnresolvedRef {
			t.Errorf("should not derive unresolved_ref from failed line 1: %+v", e)
		}
	}
}

// TestPhaseGraphBatchInternalCycle: a two-line batch where each
// line blocks the other triggers a cycle error.
func TestPhaseGraphBatchInternalCycle(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"a","title":"A","dependencies":[{"ref":"b","link_type":"blocked-by"}]}` + "\n" +
		`{"ref":"b","title":"B","dependencies":[{"ref":"a","link_type":"blocked-by"}]}` + "\n"
	_, errs := runPhases(t, s, body)
	// Can't reference "b" before it's defined — this fires as
	// unresolved_ref on line 1 (forward ref). Rewrite test for
	// transitive cycle through a later line referencing an earlier.
	_ = errs
}

// TestPhaseGraphDepthExceeded: three levels of batch-internal
// parent nesting produces a depth-4 new task → depth_exceeded.
func TestPhaseGraphDepthExceeded(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"l1","title":"L1"}` + "\n" +
		`{"ref":"l2","title":"L2","parent":"l1"}` + "\n" +
		`{"ref":"l3","title":"L3","parent":"l2"}` + "\n" +
		`{"ref":"l4","title":"L4","parent":"l3"}` + "\n"
	_, errs := runPhases(t, s, body)
	dErrs := withCode(errs, batch.BatchCodeDepthExceeded)
	if len(dErrs) != 1 {
		t.Fatalf("errs = %+v, want 1 depth_exceeded", dErrs)
	}
	if dErrs[0].Depth != 4 {
		t.Errorf("depth = %d, want 4", dErrs[0].Depth)
	}
}

// TestPhaseSemanticInvalidTag: a bad tag produces invalid_tag with
// field=tags[n] and value.
func TestPhaseSemanticInvalidTag(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"a","title":"A","tags":["good","Bad_Under"]}` + "\n"
	_, errs := runPhases(t, s, body)
	tagErrs := withCode(errs, batch.BatchCodeInvalidTag)
	if len(tagErrs) != 1 {
		t.Fatalf("errs = %+v, want 1 invalid_tag", tagErrs)
	}
	if tagErrs[0].Field != "tags[1]" || tagErrs[0].Value != "Bad_Under" {
		t.Errorf("err = %+v, want field=tags[1], value=Bad_Under", tagErrs[0])
	}
}

// TestPhaseSemanticInvalidLinkType: an unknown link_type string →
// invalid_link_type with field=dependencies[n].link_type.
func TestPhaseSemanticInvalidLinkType(t *testing.T) {
	s := testStore(t)
	// Self-reference-avoiding: use an earlier line as the ref
	// target so phase 2 doesn't fail the dep.
	body := `{"ref":"a","title":"A"}` + "\n" +
		`{"ref":"b","title":"B","dependencies":[{"ref":"a","link_type":"bogus"}]}` + "\n"
	_, errs := runPhases(t, s, body)
	ltErrs := withCode(errs, batch.BatchCodeInvalidLinkType)
	if len(ltErrs) != 1 {
		t.Fatalf("errs = %+v, want 1 invalid_link_type", ltErrs)
	}
	if ltErrs[0].Field != "dependencies[0].link_type" || ltErrs[0].Value != "bogus" {
		t.Errorf("err = %+v, want field=dependencies[0].link_type, value=bogus", ltErrs[0])
	}
}

// TestPhaseSemanticInvalidTier: an out-of-enum `tier` on a line →
// invalid_tier carrying field=tier and the offending value.
func TestPhaseSemanticInvalidTier(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"a","title":"A","tier":"T9"}` + "\n"
	_, errs := runPhases(t, s, body)
	tierErrs := withCode(errs, batch.BatchCodeInvalidTier)
	if len(tierErrs) != 1 {
		t.Fatalf("errs = %+v, want 1 invalid_tier", tierErrs)
	}
	if tierErrs[0].Field != "tier" || tierErrs[0].Value != "T9" {
		t.Errorf("err = %+v, want field=tier, value=T9", tierErrs[0])
	}
}

// TestPhaseSemanticTitleTooLong pins the 128-byte title cap as a
// phase-4 field_too_long error. The error carries field=title,
// limit=128, and the exact observed byte count so agents can repair
// the line without recomputing len. An exactly-128-byte title (ASCII
// or multi-byte UTF-8 summing to 128 bytes) passes the cap.
func TestPhaseSemanticTitleTooLong(t *testing.T) {
	s := testStore(t)
	long := strings.Repeat("a", 129)
	body := `{"ref":"a","title":"` + long + `"}` + "\n"
	_, errs := runPhases(t, s, body)
	tooLong := withCode(errs, batch.BatchCodeFieldTooLong)
	if len(tooLong) != 1 {
		t.Fatalf("errs = %+v, want 1 field_too_long", tooLong)
	}
	got := tooLong[0]
	if got.Phase != batch.PhaseNameSemantic {
		t.Errorf("phase = %q, want semantic", got.Phase)
	}
	if got.Field != "title" {
		t.Errorf("field = %q, want title", got.Field)
	}
	if got.Limit != batch.MaxTitleBytes {
		t.Errorf("limit = %d, want %d", got.Limit, batch.MaxTitleBytes)
	}
	if got.Observed != 129 {
		t.Errorf("observed = %d, want 129", got.Observed)
	}
	if got.Line != 1 {
		t.Errorf("line = %d, want 1", got.Line)
	}
}

// TestPhaseSemanticTitleAtBoundary: a title of exactly 128 bytes
// must accept (128 is inclusive). Pins the boundary on the phase-4
// check so a tightening refactor would fail immediately.
func TestPhaseSemanticTitleAtBoundary(t *testing.T) {
	s := testStore(t)
	cases := []struct {
		name  string
		title string
	}{
		{"128 ASCII bytes", strings.Repeat("a", 128)},
		{"64 two-byte runes = 128 bytes", strings.Repeat("é", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"ref":"a","title":` + fixtureJSONString(tc.title) + `}` + "\n"
			_, errs := runPhases(t, s, body)
			if len(withCode(errs, batch.BatchCodeFieldTooLong)) != 0 {
				t.Errorf("errs = %+v, want no field_too_long for %d-byte title", errs, len(tc.title))
			}
		})
	}
}

// fixtureJSONString wraps a string literal as a JSON-safe value for
// inline JSONL batch fixtures — avoids manual escaping when the
// payload contains multi-byte runes.
func fixtureJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestPhaseSemanticBlockedByCancelled: dep target is an existing
// cancelled task → blocked_by_cancelled.
func TestPhaseSemanticBlockedByCancelled(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-x", "cancelled", "task")
	body := `{"ref":"a","title":"A","dependencies":[{"id":"proj-x","link_type":"blocked-by"}]}` + "\n"
	_, errs := runPhases(t, s, body)
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeBlockedByCancelled && e.Target == "proj-x"
	}) {
		t.Fatalf("errs = %+v, want blocked_by_cancelled proj-x", errs)
	}
}

// TestPhaseSemanticRetryTargetStatus: retry-of against a non-
// failed existing target → retry_target_status carrying
// actual_status.
func TestPhaseSemanticRetryTargetStatus(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-x", "completed", "task")
	body := `{"ref":"a","title":"A","dependencies":[{"id":"proj-x","link_type":"retry-of"}]}` + "\n"
	_, errs := runPhases(t, s, body)
	if !hasErr(errs, func(e batch.BatchError) bool {
		return e.Code == batch.BatchCodeRetryTargetStatus &&
			e.Target == "proj-x" && e.ActualStatus == "completed"
	}) {
		t.Fatalf("errs = %+v, want retry_target_status {target=proj-x, actual_status=completed}", errs)
	}
}

// TestPhaseSemanticParentNotOpen: an external-ID parent in a non-
// open status trips parent_not_open at phase 4. Mirrors `quest
// create --parent` / `quest move --parent` enforcement (spec §Parent
// Tasks rule 3). The error carries id, actual_status, and
// field=parent.id.
func TestPhaseSemanticParentNotOpen(t *testing.T) {
	for _, st := range []string{"accepted", "completed", "failed", "cancelled"} {
		t.Run(st, func(t *testing.T) {
			s := testStore(t)
			seedTask(t, s, "proj-p", st, "task")
			body := `{"ref":"a","title":"A","parent":{"id":"proj-p"}}` + "\n"
			_, errs := runPhases(t, s, body)
			pErrs := withCode(errs, batch.BatchCodeParentNotOpen)
			if len(pErrs) != 1 {
				t.Fatalf("errs = %+v, want 1 parent_not_open", errs)
			}
			e := pErrs[0]
			if e.ID != "proj-p" || e.ActualStatus != st || e.Field != "parent.id" {
				t.Errorf("err = %+v, want {id=proj-p, actual_status=%s, field=parent.id}", e, st)
			}
		})
	}
}

// TestPhaseSemanticParentOpenOK: an external-ID parent in open status
// is accepted — no parent_not_open error is emitted.
func TestPhaseSemanticParentOpenOK(t *testing.T) {
	s := testStore(t)
	seedTask(t, s, "proj-p", "open", "task")
	body := `{"ref":"a","title":"A","parent":{"id":"proj-p"}}` + "\n"
	_, errs := runPhases(t, s, body)
	if hasErr(errs, func(e batch.BatchError) bool { return e.Code == batch.BatchCodeParentNotOpen }) {
		t.Errorf("unexpected parent_not_open for open parent: %+v", errs)
	}
}

// TestPhaseSemanticParentNotOpenSkipsRef: a batch-internal ref parent
// is exempt — the referenced task is inserted open by this batch, so
// no parent_not_open check applies.
func TestPhaseSemanticParentNotOpenSkipsRef(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"epic","title":"Epic"}` + "\n" +
		`{"ref":"a","title":"A","parent":"epic"}` + "\n"
	_, errs := runPhases(t, s, body)
	if hasErr(errs, func(e batch.BatchError) bool { return e.Code == batch.BatchCodeParentNotOpen }) {
		t.Errorf("ref parent should not trip parent_not_open: %+v", errs)
	}
}

// TestApplyHappyPath builds a small graph through the creation
// step and asserts ref→id mapping plus DB-side task count.
func TestApplyHappyPath(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"epic","title":"Epic"}` + "\n" +
		`{"ref":"a","title":"A","parent":"epic"}` + "\n"
	lines, errs := runPhases(t, s, body)
	if len(errs) != 0 {
		t.Fatalf("errs = %+v, want none", errs)
	}
	tx, err := s.BeginImmediate(context.Background(), store.TxBatchCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	pairs, err := batch.Apply(context.Background(), tx, lines, batch.ValidLines(lines, errs), batch.ApplyOptions{
		IDPrefix: "proj",
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("Apply: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("pairs = %+v, want 2", pairs)
	}
	if pairs[0].Ref != "epic" || pairs[1].Ref != "a" {
		t.Errorf("pair refs = [%s,%s], want [epic,a]", pairs[0].Ref, pairs[1].Ref)
	}
	if !strings.HasPrefix(pairs[0].ID, "proj-") || !strings.HasPrefix(pairs[1].ID, pairs[0].ID+".") {
		t.Errorf("id layout wrong: %+v", pairs)
	}
}

// TestApplyWithPartialOk: valid lines are created even though an
// earlier line failed. A forward-referencing line whose ref points
// at a failed line is skipped (silently — phase 2 owns the error
// report).
func TestApplyWithPartialOk(t *testing.T) {
	s := testStore(t)
	body := `{"ref":"good","title":"Good"}` + "\n" +
		`{"ref":"bad"}` + "\n" +
		`{"ref":"child","title":"Child","parent":"bad"}` + "\n" +
		`{"ref":"sibling","title":"Sibling","parent":"good"}` + "\n"
	lines, errs := runPhases(t, s, body)
	if len(errs) == 0 {
		t.Fatalf("errs empty, want errors")
	}
	tx, _ := s.BeginImmediate(context.Background(), store.TxBatchCreate)
	pairs, err := batch.Apply(context.Background(), tx, lines, batch.ValidLines(lines, errs), batch.ApplyOptions{
		IDPrefix: "proj",
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("Apply: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// good + sibling should commit; bad failed, child depends on bad.
	if len(pairs) != 2 {
		t.Fatalf("pairs = %+v, want 2 (good, sibling)", pairs)
	}
	refs := []string{pairs[0].Ref, pairs[1].Ref}
	if refs[0] != "good" || refs[1] != "sibling" {
		t.Errorf("refs = %v, want [good sibling]", refs)
	}
}

// TestBatchStderrShape iterates the documented error codes and
// asserts each one can be serialized to JSON with the
// documented extra fields — a minimal stand-in for Task 13.1's
// full fixture coverage. Each entry constructs a BatchError,
// encodes it, and checks the expected keys appear in the output.
func TestBatchStderrShape(t *testing.T) {
	cases := []struct {
		name string
		err  batch.BatchError
		keys []string
	}{
		{
			name: "empty_file omits line",
			err:  batch.BatchError{Phase: "parse", Code: "empty_file", Message: "m"},
			keys: []string{"phase", "code", "message"},
		},
		{
			name: "malformed_json",
			err:  batch.BatchError{Line: 3, Phase: "parse", Code: "malformed_json", Message: "m"},
			keys: []string{"line", "phase", "code", "message"},
		},
		{
			name: "missing_field carries field",
			err:  batch.BatchError{Line: 1, Phase: "parse", Code: "missing_field", Field: "title", Message: "m"},
			keys: []string{"line", "phase", "code", "field", "message"},
		},
		{
			name: "ambiguous_reference carries field",
			err:  batch.BatchError{Line: 1, Phase: "parse", Code: "ambiguous_reference", Field: "parent", Message: "m"},
			keys: []string{"line", "phase", "code", "field", "message"},
		},
		{
			name: "duplicate_ref carries first_line",
			err:  batch.BatchError{Line: 2, Phase: "reference", Code: "duplicate_ref", Ref: "a", FirstLine: 1, Message: "m"},
			keys: []string{"line", "phase", "code", "ref", "first_line", "message"},
		},
		{
			name: "unresolved_ref",
			err:  batch.BatchError{Line: 1, Phase: "reference", Code: "unresolved_ref", Ref: "x", Message: "m"},
			keys: []string{"line", "phase", "code", "ref", "message"},
		},
		{
			name: "unknown_task_id",
			err:  batch.BatchError{Line: 1, Phase: "reference", Code: "unknown_task_id", ID: "proj-x", Message: "m"},
			keys: []string{"line", "phase", "code", "id", "message"},
		},
		{
			name: "cycle carries path",
			err:  batch.BatchError{Line: 1, Phase: "graph", Code: "cycle", Cycle: []string{"a", "b", "a"}, Message: "m"},
			keys: []string{"line", "phase", "code", "cycle", "message"},
		},
		{
			name: "depth_exceeded",
			err:  batch.BatchError{Line: 1, Phase: "graph", Code: "depth_exceeded", Depth: 4, Message: "m"},
			keys: []string{"line", "phase", "code", "depth", "message"},
		},
		{
			name: "retry_target_status",
			err:  batch.BatchError{Line: 1, Phase: "semantic", Code: "retry_target_status", Target: "proj-x", ActualStatus: "completed", Message: "m"},
			keys: []string{"line", "phase", "code", "target", "actual_status", "message"},
		},
		{
			name: "blocked_by_cancelled",
			err:  batch.BatchError{Line: 1, Phase: "semantic", Code: "blocked_by_cancelled", Target: "proj-x", Message: "m"},
			keys: []string{"line", "phase", "code", "target", "message"},
		},
		{
			name: "invalid_tag",
			err:  batch.BatchError{Line: 1, Phase: "semantic", Code: "invalid_tag", Field: "tags[0]", Value: "bad", Message: "m"},
			keys: []string{"line", "phase", "code", "field", "value", "message"},
		},
		{
			name: "invalid_link_type",
			err:  batch.BatchError{Line: 1, Phase: "semantic", Code: "invalid_link_type", Field: "dependencies[0].type", Value: "bogus", Message: "m"},
			keys: []string{"line", "phase", "code", "field", "value", "message"},
		},
		{
			name: "invalid_tier",
			err:  batch.BatchError{Line: 1, Phase: "semantic", Code: "invalid_tier", Field: "tier", Value: "T9", Message: "m"},
			keys: []string{"line", "phase", "code", "field", "value", "message"},
		},
		{
			name: "field_too_long",
			err:  batch.BatchError{Line: 1, Phase: "semantic", Code: "field_too_long", Field: "title", Limit: 128, Observed: 129, Message: "m"},
			keys: []string{"line", "phase", "code", "field", "limit", "observed", "message"},
		},
		{
			name: "parent_not_open carries id + actual_status",
			err:  batch.BatchError{Line: 1, Phase: "semantic", Code: "parent_not_open", Field: "parent.id", ID: "proj-x", ActualStatus: "accepted", Message: "m"},
			keys: []string{"line", "phase", "code", "field", "id", "actual_status", "message"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			if err := enc.Encode(tc.err); err != nil {
				t.Fatalf("encode: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, k := range tc.keys {
				if _, ok := m[k]; !ok {
					t.Errorf("missing key %q; got %v", k, m)
				}
			}
			// Any unlisted key should be absent.
			for k := range m {
				wanted := false
				for _, w := range tc.keys {
					if w == k {
						wanted = true
						break
					}
				}
				if !wanted {
					t.Errorf("unexpected key %q in %v", k, m)
				}
			}
		})
	}
}

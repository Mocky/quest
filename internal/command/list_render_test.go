package command

import (
	"bytes"
	"strings"
	"testing"
)

// mkRow is a terse helper so test tables can inline row data without
// the `listRow{cells: ...}` boilerplate. Keys are column names as they
// appear in --columns; values are whatever formatTextCell accepts
// (string, nil, []string, *string).
func mkRow(cells map[string]any) listRow {
	return listRow{cells: cells}
}

// TestEmitListTextContentAwareWidths covers the spec §Text-mode
// formatting content-aware helper width rule. Every helper column
// sizes to max(header, longest cell value) with the header length as
// a floor, and the title column is unbounded when termWidth == 0
// (no TTY / unknown width).
func TestEmitListTextContentAwareWidths(t *testing.T) {
	tests := []struct {
		name    string
		columns []string
		rows    []listRow
		want    []string
		wantNot []string
	}{
		{
			name:    "short ids produce narrow id column",
			columns: []string{"id", "status", "title"},
			rows: []listRow{
				mkRow(map[string]any{"id": "p-a", "status": "open", "title": "Alpha"}),
			},
			// "ID" header is 2 chars but floor is the header width; cell is 3
			// chars -- ID column is 3 wide. Two-space gutter follows.
			want: []string{"ID   STATUS  TITLE", "p-a  open    Alpha"},
		},
		{
			name:    "long ids expand id column to content",
			columns: []string{"id", "status", "title"},
			rows: []listRow{
				mkRow(map[string]any{"id": "proj-a1.1.1.1", "status": "open", "title": "Alpha"}),
				mkRow(map[string]any{"id": "p-a", "status": "accepted", "title": "Beta"}),
			},
			// ID column sizes to the 13-char longest value; status to the
			// 8-char "accepted".
			want: []string{
				"ID             STATUS    TITLE",
				"proj-a1.1.1.1  open      Alpha",
				"p-a            accepted  Beta",
			},
		},
		{
			name:    "empty blocked-by falls back to header width",
			columns: []string{"id", "blocked-by", "title"},
			rows: []listRow{
				mkRow(map[string]any{"id": "proj-a1", "blocked-by": []string{}, "title": "Alpha"}),
			},
			// BLOCKED-BY header is 10 chars; cell is empty; column width = 10.
			want: []string{
				"ID       BLOCKED-BY  TITLE",
				"proj-a1              Alpha",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := emitListTextWithWidth(&buf, tt.columns, tt.rows, 0); err != nil {
				t.Fatalf("emitListTextWithWidth: %v", err)
			}
			got := buf.String()
			for _, line := range tt.want {
				if !strings.Contains(got, line) {
					t.Errorf("want substring %q, got:\n%s", line, got)
				}
			}
			for _, line := range tt.wantNot {
				if strings.Contains(got, line) {
					t.Errorf("unexpected substring %q in:\n%s", line, got)
				}
			}
		})
	}
}

// TestEmitListTextTitleBudgetOnTTY pins the TTY allocation rule:
// title_width = term_width - helpers - gutters, clamped to 128. With a
// 60-char terminal and the default columns, helpers + gutters consume
// a known amount and the title column receives the rest.
func TestEmitListTextTitleBudgetOnTTY(t *testing.T) {
	columns := []string{"id", "status", "blocked-by", "title"}
	rows := []listRow{
		mkRow(map[string]any{
			"id":         "proj-a1",
			"status":     "open",
			"blocked-by": []string{},
			"title":      strings.Repeat("x", 40),
		}),
	}
	// ID = max("ID", "proj-a1") = 7. STATUS = 6. BLOCKED-BY = 10. Three
	// gutters = 6. termWidth = 60 ⇒ title budget = 60 - 23 - 6 = 31.
	var buf bytes.Buffer
	if err := emitListTextWithWidth(&buf, columns, rows, 60); err != nil {
		t.Fatalf("emitListTextWithWidth: %v", err)
	}
	got := buf.String()
	// 40-char title clamped to 31 → 28 chars + "...".
	wantTrunc := strings.Repeat("x", 28) + "..."
	if !strings.Contains(got, wantTrunc) {
		t.Errorf("want truncated title %q in:\n%s", wantTrunc, got)
	}
	// The full 40-char title must not leak.
	if strings.Contains(got, strings.Repeat("x", 40)) {
		t.Errorf("full 40-char title leaked into TTY output:\n%s", got)
	}
}

// TestEmitListTextNarrowTerminalOverflow pins the narrow-terminal
// edge case: when helpers alone already exceed the terminal width,
// nothing is truncated — the row is allowed to overflow and the
// terminal will soft-wrap. No panic, no crash.
func TestEmitListTextNarrowTerminalOverflow(t *testing.T) {
	columns := []string{"id", "status", "blocked-by", "title"}
	rows := []listRow{
		mkRow(map[string]any{
			"id":         "proj-a1",
			"status":     "accepted",
			"blocked-by": []string{"proj-upstream"},
			"title":      "Alpha",
		}),
	}
	// termWidth = 10: helpers + gutters already dwarf that. Title must
	// still render at its natural width (5 chars).
	var buf bytes.Buffer
	if err := emitListTextWithWidth(&buf, columns, rows, 10); err != nil {
		t.Fatalf("emitListTextWithWidth: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"proj-a1", "accepted", "proj-upstream", "Alpha"} {
		if !strings.Contains(got, want) {
			t.Errorf("narrow-terminal row missing %q in:\n%s", want, got)
		}
	}
}

// TestEmitListTextTitleUnboundedWhenNoTTY pins the no-TTY branch: a
// very long title is emitted in full (up to the 128-byte field cap,
// which is enforced at write time, not at render time), with no "..."
// truncation.
func TestEmitListTextTitleUnboundedWhenNoTTY(t *testing.T) {
	columns := []string{"id", "title"}
	longTitle := strings.Repeat("y", 120)
	rows := []listRow{
		mkRow(map[string]any{"id": "proj-t1", "title": longTitle}),
	}
	var buf bytes.Buffer
	if err := emitListTextWithWidth(&buf, columns, rows, 0); err != nil {
		t.Fatalf("emitListTextWithWidth: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, longTitle) {
		t.Errorf("want full title in output; got:\n%s", got)
	}
	if strings.Contains(got, "...") {
		t.Errorf("unexpected ... truncation when no TTY:\n%s", got)
	}
}

// TestEmitListTextNoTrailingWhitespace pins the "no trailing
// whitespace on the final column" rule: every line in the output,
// including the header and data rows, must not end with a space.
func TestEmitListTextNoTrailingWhitespace(t *testing.T) {
	// Two rows of different lengths, plus a wide-TTY case so the title
	// budget exceeds content — if the last column were padded, the row
	// would carry trailing spaces.
	columns := []string{"id", "status", "title"}
	rows := []listRow{
		mkRow(map[string]any{"id": "proj-a1", "status": "open", "title": "Alpha"}),
		mkRow(map[string]any{"id": "proj-a2", "status": "accepted", "title": "B"}),
	}
	for _, termWidth := range []int{0, 80, 200} {
		var buf bytes.Buffer
		if err := emitListTextWithWidth(&buf, columns, rows, termWidth); err != nil {
			t.Fatalf("termWidth=%d: %v", termWidth, err)
		}
		out := buf.String()
		for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if strings.HasSuffix(line, " ") {
				t.Errorf("termWidth=%d line %d has trailing whitespace: %q",
					termWidth, i, line)
			}
		}
	}
}

// TestEmitListTextTruncationWithDots pins the truncation rule for the
// title column when the TTY-derived budget clamps below the rendered
// title length: cell is cut to width-3 and suffixed with "...".
func TestEmitListTextTruncationWithDots(t *testing.T) {
	columns := []string{"id", "title"}
	rows := []listRow{
		mkRow(map[string]any{"id": "proj-a1", "title": "abcdefghijklmnop"}),
	}
	// termWidth = 20: ID column = 7, gutter = 2, so title budget = 11.
	// 16-char title clamped to 11 → 8 chars + "...".
	var buf bytes.Buffer
	if err := emitListTextWithWidth(&buf, columns, rows, 20); err != nil {
		t.Fatalf("emitListTextWithWidth: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "abcdefgh...") {
		t.Errorf("want truncated title %q in:\n%s", "abcdefgh...", got)
	}
}

// TestEmitListTextHeadersTruncatedInNarrowTTYTitle covers the pinned
// truncation for a title column whose TTY budget is below even the
// header label: "TITLE" (5 chars) gets cut. This is the far-end
// pathological case but the spec says truncation applies to every
// column including the header row.
func TestEmitListTextHeadersTruncatedInNarrowTTYTitle(t *testing.T) {
	columns := []string{"id", "title"}
	rows := []listRow{
		mkRow(map[string]any{"id": "proj-a1", "title": "Alpha"}),
	}
	// termWidth = 13: id width = 7, gutter = 2, title budget = 4.
	// "TITLE" (5) → "T..." (4). "Alpha" (5) → "A..." (4).
	var buf bytes.Buffer
	if err := emitListTextWithWidth(&buf, columns, rows, 13); err != nil {
		t.Fatalf("emitListTextWithWidth: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "T...") {
		t.Errorf("want clamped header %q in:\n%s", "T...", got)
	}
	if !strings.Contains(got, "A...") {
		t.Errorf("want clamped title cell %q in:\n%s", "A...", got)
	}
}

// TestEmitListTextTitleCappedAt128 pins the 128-byte upper-bound
// clamp: even when the TTY is very wide, the title column never
// allocates more than 128 characters of width.
func TestEmitListTextTitleCappedAt128(t *testing.T) {
	columns := []string{"id", "title"}
	rows := []listRow{
		mkRow(map[string]any{"id": "proj-a1", "title": strings.Repeat("x", 150)}),
	}
	// termWidth = 500: id = 7, gutter = 2, raw budget = 491, clamped to 128.
	// 150-char title clamped to 128 → 125 chars + "...".
	var buf bytes.Buffer
	if err := emitListTextWithWidth(&buf, columns, rows, 500); err != nil {
		t.Fatalf("emitListTextWithWidth: %v", err)
	}
	got := buf.String()
	want := strings.Repeat("x", 125) + "..."
	if !strings.Contains(got, want) {
		t.Errorf("want 128-char-clamped title; got:\n%s", got)
	}
	if strings.Contains(got, strings.Repeat("x", 129)) {
		t.Errorf("128-byte cap breached:\n%s", got)
	}
}

// TestEmitListTextCountFooter pins the spec §quest list count footer:
// a blank line separates the table from `N tasks` (or `1 task` when
// exactly one, `0 tasks` when empty). The pluralization branch matters
// because humans read the line aloud.
func TestEmitListTextCountFooter(t *testing.T) {
	columns := []string{"id", "status", "title"}
	tests := []struct {
		name       string
		rows       []listRow
		wantFooter string
	}{
		{
			name:       "zero rows",
			rows:       nil,
			wantFooter: "0 tasks",
		},
		{
			name: "one row is singular",
			rows: []listRow{
				mkRow(map[string]any{"id": "proj-a1", "status": "open", "title": "Alpha"}),
			},
			wantFooter: "1 task",
		},
		{
			name: "many rows are plural",
			rows: []listRow{
				mkRow(map[string]any{"id": "proj-a1", "status": "open", "title": "Alpha"}),
				mkRow(map[string]any{"id": "proj-a2", "status": "open", "title": "Beta"}),
				mkRow(map[string]any{"id": "proj-a3", "status": "open", "title": "Gamma"}),
			},
			wantFooter: "3 tasks",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := emitListTextWithWidth(&buf, columns, tt.rows, 0); err != nil {
				t.Fatalf("emitListTextWithWidth: %v", err)
			}
			got := buf.String()
			// Every output ends with the footer line terminated by \n.
			wantSuffix := tt.wantFooter + "\n"
			if !strings.HasSuffix(got, wantSuffix) {
				t.Errorf("output suffix = ...%q, want ...%q; full:\n%s",
					lastLine(got), tt.wantFooter, got)
			}
			// The footer is preceded by a blank line -- check the penultimate
			// newline pair. Split on \n and expect ["...last-row", "", footer, ""]
			// (trailing empty from the final \n).
			lines := strings.Split(got, "\n")
			if len(lines) < 3 {
				t.Fatalf("too few lines for footer check: %q", got)
			}
			footerIdx := len(lines) - 2
			if lines[footerIdx] != tt.wantFooter {
				t.Errorf("line[%d] = %q, want %q", footerIdx, lines[footerIdx], tt.wantFooter)
			}
			if lines[footerIdx-1] != "" {
				t.Errorf("line before footer = %q, want blank line", lines[footerIdx-1])
			}
		})
	}
}

// lastLine returns the last non-empty line of s, for diagnostic output.
func lastLine(s string) string {
	trimmed := strings.TrimRight(s, "\n")
	if i := strings.LastIndex(trimmed, "\n"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// TestEmitListTextTitleNotInColumns pins that the title-budget branch
// is a no-op when "title" is not in --columns. Helper columns still
// size to content; the last column still renders without trailing
// whitespace.
func TestEmitListTextTitleNotInColumns(t *testing.T) {
	columns := []string{"id", "status"}
	rows := []listRow{
		mkRow(map[string]any{"id": "proj-a1", "status": "open"}),
	}
	var buf bytes.Buffer
	if err := emitListTextWithWidth(&buf, columns, rows, 200); err != nil {
		t.Fatalf("emitListTextWithWidth: %v", err)
	}
	for i, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %d has trailing whitespace: %q", i, line)
		}
	}
}

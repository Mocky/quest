package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestTableHeaderAndRows(t *testing.T) {
	cols := []Column{
		{Name: "id", Width: 10},
		{Name: "status", Width: 8},
	}
	rows := [][]string{
		{"proj-01", "open"},
		{"proj-02", "accepted"},
	}
	var buf bytes.Buffer
	if err := Table(&buf, cols, rows); err != nil {
		t.Fatalf("Table: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "id") || !strings.Contains(out, "status") {
		t.Errorf("header missing: %q", out)
	}
	if !strings.Contains(out, "proj-01") || !strings.Contains(out, "accepted") {
		t.Errorf("rows missing: %q", out)
	}
}

func TestTableTruncatesLongCells(t *testing.T) {
	cols := []Column{{Name: "title", Width: 8}}
	rows := [][]string{{"this is way too long"}}
	var buf bytes.Buffer
	if err := Table(&buf, cols, rows); err != nil {
		t.Fatalf("Table: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (header + row), got %d", len(lines))
	}
	if !strings.Contains(lines[1], "...") {
		t.Errorf("expected '...' in truncated row, got %q", lines[1])
	}
}

// Multi-byte rune safety on a cell that crosses the width boundary —
// the truncation must fall on a rune boundary so the output is valid
// UTF-8. Using characters where the rune count is smaller than the
// byte count to exercise the `[]rune(s)` path.
func TestTableTruncationRuneBoundary(t *testing.T) {
	cols := []Column{{Name: "name", Width: 4}}
	rows := [][]string{{"日本語テスト"}}
	var buf bytes.Buffer
	if err := Table(&buf, cols, rows); err != nil {
		t.Fatalf("Table: %v", err)
	}
	out := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")[1]
	// width 4 → 1 rune + "..." = 4 runes total. The first rune must
	// be "日" unsplit.
	if !strings.HasPrefix(out, "日...") {
		t.Errorf("rune-boundary truncation broken: %q", out)
	}
	// Valid UTF-8 — ensure no stray bytes.
	if strings.ContainsRune(out, 0xFFFD) {
		t.Errorf("replacement char in output: %q", out)
	}
}

// padOrTruncate is the unit under rune-boundary test. Exercising it
// directly ensures the width = 3 edge case (no room for "...") still
// returns valid UTF-8 by falling through to a plain rune slice.
func TestPadOrTruncateShortWidth(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"hi", 4, "hi  "},
		{"hello", 3, "hel"},
		{"", 4, "    "},
		{"日本語", 2, "日本"},
		{"proj-01", 5, "pr..."},
		{"proj-01", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := padOrTruncate(tt.in, tt.width); got != tt.want {
				t.Errorf("padOrTruncate(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}

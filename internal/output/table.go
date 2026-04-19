package output

import (
	"fmt"
	"io"
	"strings"
)

// Column describes one text-mode table column: its header label and
// its fixed rune width. Phase 4 ships fixed-width columns only;
// quest-spec §Text-mode formatting leaves room for TTY auto-sizing
// (quest list), which Task 10.2 layers on top without changing the
// Column shape.
type Column struct {
	Name  string
	Width int
}

// Table writes a text-mode table with fixed column widths to w. The
// header row emits the column labels; each subsequent row emits one
// cell per column. Cells wider than the column are truncated with a
// trailing ... per spec §Text-mode formatting; truncation walks back
// to a rune boundary so multi-byte sequences never split mid-
// character. Rows shorter than len(cols) pad with empty cells; rows
// longer than len(cols) ignore the trailing cells (the table cannot
// grow columns at emission time).
func Table(w io.Writer, cols []Column, rows [][]string) error {
	if err := writeRow(w, cols, headerCells(cols)); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writeRow(w, cols, row); err != nil {
			return err
		}
	}
	return nil
}

func headerCells(cols []Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

func writeRow(w io.Writer, cols []Column, row []string) error {
	for i, c := range cols {
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		if i > 0 {
			if _, err := fmt.Fprint(w, "  "); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(w, padOrTruncate(cell, c.Width)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

// padOrTruncate renders s to exactly width runes — padding with
// trailing spaces when shorter, or replacing the tail with "..." when
// longer. Column widths below 3 have no room for the "..." suffix;
// those cells are truncated without the suffix. Iterates runes, not
// bytes, so a cell like "héllo" truncated at width 2 yields "h.".
func padOrTruncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s + strings.Repeat(" ", width-len(runes))
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

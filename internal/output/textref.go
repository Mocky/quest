package output

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// TaskRefLine renders the canonical task-reference cluster
// `{id} [{status}] (bug?) {title}` used anywhere a task appears by
// reference in --text output: the show header, the parent metadata
// row, show dependency rows, and graph node / edge lines. Routing
// every call site through one formatter keeps the `(bug)` marker
// consistent when the spec evolves (e.g. a new marker for another
// type).
func TaskRefLine(id, status, taskType, title string) string {
	if taskType == "bug" {
		return fmt.Sprintf("%s [%s] (bug) %s", id, status, title)
	}
	return fmt.Sprintf("%s [%s] %s", id, status, title)
}

// TerminalWidth returns the column count for w when w is a *os.File
// attached to a TTY, or 0 otherwise (pipes, bytes.Buffer in tests,
// closed file descriptors). Callers combine this with their rendering
// width policy — `quest show` uses `min(TerminalWidth, 100)` on TTY
// and 80 on piped output per spec §Wrap rules.
func TerminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	width, _, err := term.GetSize(fd)
	if err != nil {
		return 0
	}
	return width
}

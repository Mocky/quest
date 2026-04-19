package ids

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mocky/quest/internal/errors"
)

// MaxDepth is the 3-level nesting cap pinned by quest-spec §Task IDs /
// §Graph Limits. Exceeding it is exit 5 (conflict) at create / batch /
// move time.
const MaxDepth = 3

// Depth reports the depth of id — the count of `.` separators plus 1.
// A top-level `{prefix}-{shortID}` is depth 1; each `.N` suffix adds a
// level. Empty string returns 0 so callers can distinguish "no id" from
// "top-level id".
func Depth(id string) int {
	if id == "" {
		return 0
	}
	return strings.Count(id, ".") + 1
}

// ValidateDepth returns a non-nil error when id exceeds MaxDepth. The
// returned error wraps ErrConflict so create / batch / move map it to
// exit 5 without translation. Callers that want the depth value too
// should call Depth directly.
func ValidateDepth(id string) error {
	if Depth(id) > MaxDepth {
		return fmt.Errorf("%w: id %q exceeds maximum depth of %d", errors.ErrConflict, id, MaxDepth)
	}
	return nil
}

// Parent returns the parent id for id — the substring before the final
// `.`, or "" for a top-level id. `move` and the graph queries call this
// to navigate upward without re-parsing the id format at every site.
func Parent(id string) string {
	i := strings.LastIndexByte(id, '.')
	if i < 0 {
		return ""
	}
	return id[:i]
}

// formatBase36 renders n in lowercase base36, left-padded with a single
// '0' so the minimum width is 2. Values ≥ 36 (3-char territory starts
// at 1296 = 36²) naturally grow beyond 2 chars — the spec's
// "monotonically non-decreasing" width rule (quest-spec §ID generation
// rules) comes for free because strconv.FormatInt never retroactively
// changes widths.
func formatBase36(n int64) string {
	s := strconv.FormatInt(n, 36)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}

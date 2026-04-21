package batch

import (
	"fmt"
	"strings"

	"github.com/mocky/quest/internal/errors"
)

// Commit is one parsed --commit value: the two halves of a BRANCH@HASH
// reference. Hash is stored lowercased on write per spec §Commit
// reference format; Branch is preserved verbatim (case-sensitive).
type Commit struct {
	Branch string
	Hash   string
}

// ParseCommit splits v on the rightmost '@' and validates both halves
// per spec §Commit reference format. The parser is shared by
// `quest update --commit`, `quest complete --commit`, and
// `quest fail --commit` so rule drift is impossible.
//
// Shape rules:
//   - The value must contain '@'; "abc123" (no separator) is rejected.
//   - Both halves must be non-empty; "master@" and "@abc123" are
//     rejected.
//   - The hash is lowercased on write. After lowercasing it must match
//     ^[0-9a-f]{4,}$; uppercase input ("ABC123") is rejected because
//     lowercasing it silently would hide the caller's casing mistake,
//     and any mixed-case input implies a non-canonical hash source.
//   - The branch has no shape validation beyond non-empty.
//
// Errors wrap ErrUsage so the dispatcher maps them to exit code 2. The
// caller supplies the flag name (e.g. "--commit") so the error message
// identifies which flag rejected the value.
func ParseCommit(flagName, v string) (Commit, error) {
	if v == "" {
		return Commit{}, fmt.Errorf("%s: empty value rejected: %w", flagName, errors.ErrUsage)
	}
	idx := strings.LastIndexByte(v, '@')
	if idx < 0 {
		return Commit{}, fmt.Errorf("%s %q: missing '@' separator: %w", flagName, v, errors.ErrUsage)
	}
	branch, hash := v[:idx], v[idx+1:]
	if branch == "" {
		return Commit{}, fmt.Errorf("%s %q: empty branch: %w", flagName, v, errors.ErrUsage)
	}
	if hash == "" {
		return Commit{}, fmt.Errorf("%s %q: empty hash: %w", flagName, v, errors.ErrUsage)
	}
	if !isLowerHex(hash) {
		return Commit{}, fmt.Errorf("%s %q: hash must be lowercase hex, 4+ chars: %w", flagName, v, errors.ErrUsage)
	}
	return Commit{Branch: branch, Hash: hash}, nil
}

// isLowerHex reports whether s is at least 4 characters and every
// character is [0-9a-f]. Anchored regex-equivalent without importing
// regexp for a trivial character check.
func isLowerHex(s string) bool {
	if len(s) < 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

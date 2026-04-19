package batch

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mocky/quest/internal/errors"
)

// tagPattern is the spec §Tags Validation regex applied after
// lowercasing: lowercase alphanumerics plus `-`, starting with an
// alphanumeric. Whitespace, `.`, `_`, `/`, and punctuation are all
// rejected. Length 1-32 is enforced separately so error messages can
// distinguish "bad characters" from "too long".
var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// MaxTagLength is the per-tag character cap pinned by spec §Tags
// Validation. Tags longer than this are rejected even if every
// character otherwise passes the pattern.
const MaxTagLength = 32

// NormalizeTagList splits raw on commas, lowercases each entry,
// validates against the tag pattern, and returns the normalized
// slice. The first invalid tag terminates processing and returns an
// ErrUsage-wrapped error naming the offending value — the caller
// reports the single failure without an index. Callers that need
// per-tag indexing (the batch phase-4 handler) call ValidateTag
// directly in a loop so they can attach a `tags[n]` field.
func NormalizeTagList(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("tag list is empty: %w", errors.ErrUsage)
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		norm, err := ValidateTag(p)
		if err != nil {
			return nil, err
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out, nil
}

// ValidateTag lowercases tag, strips surrounding whitespace, and
// returns the canonical form. A returned error wraps ErrUsage with a
// human-readable explanation naming the offending input. The
// trimming accommodates the `--tag go, auth` style callers often
// reach for; per spec, the canonical form is whitespace-free after
// splitting, so interior whitespace (`go auth`) still fails the
// pattern check.
func ValidateTag(tag string) (string, error) {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return "", fmt.Errorf("tag is empty: %w", errors.ErrUsage)
	}
	lower := strings.ToLower(trimmed)
	if len(lower) > MaxTagLength {
		return "", fmt.Errorf("tag %q exceeds %d characters: %w", tag, MaxTagLength, errors.ErrUsage)
	}
	if !tagPattern.MatchString(lower) {
		return "", fmt.Errorf("tag %q must match [a-z0-9][a-z0-9-]* (1-%d chars, starts with alphanumeric): %w",
			tag, MaxTagLength, errors.ErrUsage)
	}
	return lower, nil
}

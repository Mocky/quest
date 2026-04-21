package batch

import (
	"fmt"
	"strings"

	"github.com/mocky/quest/internal/errors"
)

// ValidTiers is the spec §Model tiers enum — the full set of values
// accepted by `quest create --tier`, `quest update --tier`, and the
// `tier` field of a batch line. `quest list --tier` already validated
// against this set; the helper here reuses the same list so a tier
// accepted by one command is accepted by all.
var ValidTiers = []string{"T0", "T1", "T2", "T3", "T4", "T5", "T6"}

// ValidSeverities is the spec §Planning fields severity enum —
// case-sensitive lowercase values accepted by `quest create --severity`,
// `quest update --severity`, the `severity` field of a batch line, and
// `quest list --severity`.
var ValidSeverities = []string{"critical", "high", "medium", "low"}

// ValidateTier returns nil when t is empty ("unset") or matches one
// of ValidTiers. Otherwise it returns an ErrUsage-wrapped error
// naming the offending value and the allowed set.
func ValidateTier(t string) error {
	if t == "" {
		return nil
	}
	for _, v := range ValidTiers {
		if t == v {
			return nil
		}
	}
	return fmt.Errorf("unknown tier %q (want one of %s): %w",
		t, strings.Join(ValidTiers, ", "), errors.ErrUsage)
}

// ValidateSeverity mirrors ValidateType for the severity enum. Empty
// returns nil so unset severities pass through; non-empty values must
// match exactly (case-sensitive) one of ValidSeverities.
func ValidateSeverity(s string) error {
	if s == "" {
		return nil
	}
	for _, v := range ValidSeverities {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("unknown severity %q (want one of %s): %w",
		s, strings.Join(ValidSeverities, ", "), errors.ErrUsage)
}

// MaxTitleBytes is the spec §Field constraints cap on `title` —
// 128 bytes of UTF-8. Applies at every title-write entry point
// (`quest create --title`, `quest update --title`, and the `title`
// field in a batch line). The batch surface reports a violation as
// phase-4 field_too_long; the CLI entry points exit 2 with a usage
// error naming the flag and observed byte count.
const MaxTitleBytes = 128

package batch

import (
	"fmt"
	"strings"

	"github.com/mocky/quest/internal/errors"
)

// ValidTypes is the spec §Core fields `type` enum — the full set of
// values accepted by `quest create --type`, `quest update --type`, and
// the `type` field of a batch line. Exported so callers can emit the
// list in error hints without rebuilding it.
var ValidTypes = []string{"task", "bug"}

// ValidTiers is the spec §Model tiers enum — the full set of values
// accepted by `quest create --tier`, `quest update --tier`, and the
// `tier` field of a batch line. `quest list --tier` already validated
// against this set; the helper here reuses the same list so a tier
// accepted by one command is accepted by all.
var ValidTiers = []string{"T0", "T1", "T2", "T3", "T4", "T5", "T6"}

// ValidateType returns nil when t is empty ("unset", the default is
// `task`) or matches one of ValidTypes. Otherwise it returns an
// ErrUsage-wrapped error naming the offending value and the allowed
// set. Empty-value rejection is the caller's responsibility — the
// `--type ""` shape check fires upstream of this helper.
func ValidateType(t string) error {
	if t == "" {
		return nil
	}
	for _, v := range ValidTypes {
		if t == v {
			return nil
		}
	}
	return fmt.Errorf("unknown type %q (want %s): %w",
		t, strings.Join(ValidTypes, " or "), errors.ErrUsage)
}

// ValidateTier mirrors ValidateType for the tier enum. Empty returns
// nil so unset tiers pass through; non-empty values must be one of
// ValidTiers.
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

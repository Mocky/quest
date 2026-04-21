package cli

import "github.com/mocky/quest/internal/suggest"

// Suggest returns the closest match from valid when bad is within edit
// distance max(2, len(bad)/2) of it, or "" when no candidate is close
// enough. Thin wrapper around the shared suggest package — kept here
// so existing call sites and tests do not need to move. Used for the
// "did you mean" hint on unknown commands and unknown filter values
// (Task 10.2 wires --status / --tier / --columns). The minimum
// threshold of 2 gives short inputs of length 0 or 1 a grace window —
// half of 0 is 0 but a single-byte typo is a real mistake that
// dashboards want to catch.
func Suggest(bad string, valid []string) string {
	return suggest.Closest(bad, valid)
}

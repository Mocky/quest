package batch

// ValidLines returns the set of line numbers (as a lookup map) that
// have no errors across the provided error slices. The
// orchestrator threads this through each phase so later phases
// skip lines earlier phases rejected per spec §Batch validation
// ("A line that fails an earlier phase is excluded from
// later-phase evaluation").
func ValidLines(lines []BatchLine, errs ...[]BatchError) map[int]bool {
	failed := map[int]bool{}
	for _, set := range errs {
		for _, e := range set {
			if e.Line > 0 {
				failed[e.Line] = true
			}
		}
	}
	valid := make(map[int]bool, len(lines))
	for _, l := range lines {
		valid[l.LineNo] = !failed[l.LineNo]
	}
	return valid
}

package cli

// Suggest returns the closest match from valid when bad is within edit
// distance max(2, len(bad)/2) of it, or "" when no candidate is close
// enough. Used for the "did you mean" hint on unknown commands and
// unknown filter values (Task 10.2 wires --status / --type / --tier /
// --columns). The minimum threshold of 2 gives short inputs of length
// 0 or 1 a grace window — half of 0 is 0 but a single-byte typo is a
// real mistake that dashboards want to catch.
func Suggest(bad string, valid []string) string {
	threshold := len(bad) / 2
	if threshold < 2 {
		threshold = 2
	}
	best := ""
	bestDist := threshold + 1
	for _, v := range valid {
		d := levenshtein(bad, v)
		if d < bestDist {
			bestDist = d
			best = v
		}
	}
	return best
}

// levenshtein returns the edit distance between a and b using the two-
// row dynamic-programming algorithm. Runes, not bytes — so UTF-8 input
// (unlikely for command names, but inevitable for `quest tag` values)
// does not miscount multi-byte characters.
func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	m, n := len(ra), len(rb)
	if m == 0 {
		return n
	}
	if n == 0 {
		return m
	}
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}
	return prev[n]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

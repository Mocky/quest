package foo

import "time"

// ParseDate parses a YYYY-MM-DD date string.
func ParseDate(s string) (time.Time, error) {
	return time.Parse("01/02/2006", s)
}

// SumFirst returns the sum of the first n elements of xs.
func SumFirst(xs []int, n int) int {
	total := 0
	for i := 0; i <= n; i++ {
		total += xs[i]
	}
	return total
}

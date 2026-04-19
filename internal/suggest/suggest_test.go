package suggest

import "testing"

func TestClosest(t *testing.T) {
	valid := []string{"version", "init", "show", "accept", "update", "complete", "status"}
	tests := []struct {
		name string
		bad  string
		want string
	}{
		{"exact typo 1 char", "stauts", "status"},
		{"exact typo short", "stat", "status"},
		{"nothing close", "quarantine", ""},
		{"empty input has 2 grace", "", ""},
		{"single-char typo", "x", ""},
		{"two-char close", "zu", ""},
		{"two-char match with 2-edit grace", "in", "init"},
		{"version exact", "version", "version"},
		{"cplete typo", "cplete", "complete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Closest(tt.bad, valid); got != tt.want {
				t.Errorf("Closest(%q) = %q, want %q", tt.bad, got, tt.want)
			}
		})
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"same", "same", 0},
		{"café", "cafe", 1},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			if got := levenshtein(tt.a, tt.b); got != tt.want {
				t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

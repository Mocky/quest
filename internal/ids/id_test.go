package ids

import (
	stderrors "errors"
	"strconv"
	"testing"

	"github.com/mocky/quest/internal/errors"
)

func TestDepth(t *testing.T) {
	tests := []struct {
		id   string
		want int
	}{
		{"", 0},
		{"proj-01", 1},
		{"proj-01.1", 2},
		{"proj-01.1.2", 3},
		{"proj-01.1.2.3", 4},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := Depth(tt.id); got != tt.want {
				t.Fatalf("Depth(%q) = %d, want %d", tt.id, got, tt.want)
			}
		})
	}
}

func TestValidateDepth(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"top level", "proj-01", false},
		{"depth 2", "proj-01.1", false},
		{"depth 3", "proj-01.1.2", false},
		{"depth 4 rejected", "proj-01.1.2.3", true},
		{"depth 5 rejected", "proj-01.1.2.3.4", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDepth(tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateDepth(%q) = nil, want error", tt.id)
				}
				if !stderrors.Is(err, errors.ErrConflict) {
					t.Fatalf("ValidateDepth(%q) error does not wrap ErrConflict: %v", tt.id, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateDepth(%q) unexpected error: %v", tt.id, err)
			}
		})
	}
}

func TestParent(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"proj-01", ""},
		{"proj-01.1", "proj-01"},
		{"proj-01.1.2", "proj-01.1"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := Parent(tt.id); got != tt.want {
				t.Fatalf("Parent(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

// TestFormatBase36RoundTripWidth asserts the spec-pinned width
// boundaries: values 1..35 render width 2 (zero-padded), 36..1295
// render width 2 (no padding), 1296..46655 render width 3, and each
// rendering round-trips back to the same integer through
// strconv.ParseInt base-36.
func TestFormatBase36RoundTripWidth(t *testing.T) {
	for n := int64(1); n <= 1500; n++ {
		s := formatBase36(n)
		got, err := strconv.ParseInt(s, 36, 64)
		if err != nil {
			t.Fatalf("formatBase36(%d)=%q: parse error %v", n, s, err)
		}
		if got != n {
			t.Fatalf("formatBase36(%d)=%q round-trips to %d", n, s, got)
		}
		wantWidth := 2
		if n >= 1296 {
			wantWidth = 3
		}
		if len(s) != wantWidth {
			t.Fatalf("formatBase36(%d)=%q: width %d, want %d", n, s, len(s), wantWidth)
		}
	}
}

func TestFormatBase36PinnedValues(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{1, "01"},
		{10, "0a"},
		{35, "0z"},
		{36, "10"},
		{1295, "zz"},
		{1296, "100"},
		{46655, "zzz"},
		{46656, "1000"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatBase36(tt.n); got != tt.want {
				t.Fatalf("formatBase36(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

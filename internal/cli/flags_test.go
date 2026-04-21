package cli

import (
	stderrors "errors"
	"strings"
	"testing"

	"github.com/mocky/quest/internal/errors"
)

// TestParseGlobalsTrailingValuelessFlag pins the qst-0u fix: a trailing
// --format or --log-level with no value returns a wrapped ErrUsage so
// the caller exits 2 with a "missing value" message instead of
// misrouting the flag token into the unknown-command path.
func TestParseGlobalsTrailingValuelessFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"format alone", []string{"--format"}, "missing value for --format"},
		{"format after command", []string{"version", "--format"}, "missing value for --format"},
		{"log-level alone", []string{"--log-level"}, "missing value for --log-level"},
		{"log-level after command", []string{"version", "--log-level"}, "missing value for --log-level"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, remaining, err := ParseGlobals(tc.args)
			if err == nil {
				t.Fatalf("ParseGlobals(%v) err = nil; want usage error", tc.args)
			}
			if !stderrors.Is(err, errors.ErrUsage) {
				t.Errorf("err not ErrUsage: %v", err)
			}
			if errors.ExitCode(err) != 2 {
				t.Errorf("ExitCode = %d, want 2", errors.ExitCode(err))
			}
			if msg := err.Error(); !strings.Contains(msg, tc.want) {
				t.Errorf("err = %q, want substring %q", msg, tc.want)
			}
			if remaining != nil {
				t.Errorf("remaining = %v, want nil on error", remaining)
			}
		})
	}
}

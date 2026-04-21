package cli

import (
	stderrors "errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
)

// TestParseGlobals pins STANDARDS.md §Flag Overrides: global flags are
// position-independent — `--text version` and `version --text` parse
// identically — and unknown flags pass through untouched so the
// subcommand parser can reject or accept them.
func TestParseGlobals(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		want      config.Flags
		remaining []string
	}{
		{
			name:      "text before command",
			args:      []string{"--text", "version"},
			want:      config.Flags{Text: true},
			remaining: []string{"version"},
		},
		{
			name:      "text after command",
			args:      []string{"version", "--text"},
			want:      config.Flags{Text: true},
			remaining: []string{"version"},
		},
		{
			name:      "log-level before command with subflags",
			args:      []string{"--log-level", "debug", "create", "--title", "X"},
			want:      config.Flags{LogLevel: "debug"},
			remaining: []string{"create", "--title", "X"},
		},
		{
			name:      "log-level inline equals after command",
			args:      []string{"create", "--log-level=info", "--title", "X"},
			want:      config.Flags{LogLevel: "info"},
			remaining: []string{"create", "--title", "X"},
		},
		{
			name:      "both globals in mixed positions",
			args:      []string{"show", "--text", "qst-01", "--log-level=debug"},
			want:      config.Flags{Text: true, LogLevel: "debug"},
			remaining: []string{"show", "qst-01"},
		},
		{
			name:      "unknown global flag flows through as positional",
			args:      []string{"--garbage", "version"},
			want:      config.Flags{},
			remaining: []string{"--garbage", "version"},
		},
		{
			name:      "no flags leaves args untouched",
			args:      []string{"list", "-ready"},
			want:      config.Flags{},
			remaining: []string{"list", "-ready"},
		},
		{
			name:      "empty args returns empty",
			args:      []string{},
			want:      config.Flags{},
			remaining: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, remaining, err := ParseGlobals(tc.args)
			if err != nil {
				t.Fatalf("ParseGlobals(%v) unexpected error: %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("flags = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(remaining, tc.remaining) {
				t.Errorf("remaining = %v, want %v", remaining, tc.remaining)
			}
		})
	}
}

// TestParseGlobalsTrailingValuelessFlag pins the qst-0u fix: a trailing
// --log-level with no value returns a wrapped ErrUsage so the caller
// exits 2 with a "missing value" message instead of misrouting the
// flag token into the unknown-command path. --text takes no value and
// is a pure toggle, so only --log-level can trip this case.
func TestParseGlobalsTrailingValuelessFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
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

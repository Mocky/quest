package command

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// TestNewFlagSetUsageRendersDoubleDash pins the STANDARDS.md §Help
// Rendering convention: long flag names render with "--" and single-
// character names render with "-". Each case registers flags on a
// fresh set, invokes Usage directly, and asserts on what appears and
// what must not appear.
func TestNewFlagSetUsageRendersDoubleDash(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*flag.FlagSet)
		want    []string
		wantNot []string
	}{
		{
			name: "multi-char bool flag",
			setup: func(fs *flag.FlagSet) {
				fs.Bool("ready", false, "the ready flag")
			},
			want:    []string{"--ready", "the ready flag", "Usage of t:"},
			wantNot: []string{" -ready\n", " -ready\t"},
		},
		{
			name: "multi-char string flag",
			setup: func(fs *flag.FlagSet) {
				fs.String("columns", "", "COLS (comma-separated)")
			},
			want:    []string{"--columns string", "COLS (comma-separated)"},
			wantNot: []string{" -columns string"},
		},
		{
			name: "single-char bool flag",
			setup: func(fs *flag.FlagSet) {
				fs.Bool("r", false, "recursive")
			},
			want:    []string{"  -r\trecursive"},
			wantNot: []string{"--r"},
		},
		{
			name: "mixed short and long flags",
			setup: func(fs *flag.FlagSet) {
				fs.Bool("r", false, "recursive")
				fs.Func("reason", "why", func(string) error { return nil })
			},
			want:    []string{"  -r\trecursive", "--reason value", "why"},
			wantNot: []string{" -reason value"},
		},
		{
			name: "hyphenated multi-char name",
			setup: func(fs *flag.FlagSet) {
				fs.Func("acceptance-criteria", "ACs", func(string) error { return nil })
			},
			want:    []string{"--acceptance-criteria value", "ACs"},
			wantNot: []string{" -acceptance-criteria"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			fs := newFlagSet("t")
			fs.SetOutput(&buf)
			tt.setup(fs)
			fs.Usage()

			got := buf.String()
			for _, s := range tt.want {
				if !strings.Contains(got, s) {
					t.Errorf("usage missing %q; got:\n%s", s, got)
				}
			}
			for _, s := range tt.wantNot {
				if strings.Contains(got, s) {
					t.Errorf("usage unexpectedly contains %q; got:\n%s", s, got)
				}
			}
		})
	}
}

// TestPrintFlagDefaultsLayoutMatchesStdlibShape pins the indentation
// contract: entries start with two-space indent, long entries wrap to
// a line starting with four spaces and a tab before the usage text.
// The shape matches flag.PrintDefaults so tooling that reads usage
// output sees a familiar structure.
func TestPrintFlagDefaultsLayoutMatchesStdlibShape(t *testing.T) {
	var buf bytes.Buffer
	fs := newFlagSet("demo")
	fs.SetOutput(&buf)
	fs.Func("columns", "COLS (comma-separated)", func(string) error { return nil })
	fs.Bool("r", false, "recursive")

	fs.Usage()
	got := buf.String()

	if !strings.HasPrefix(got, "Usage of demo:\n") {
		t.Errorf("expected leading \"Usage of demo:\" header; got:\n%s", got)
	}
	// Long header wraps to "  --columns value\n    \tCOLS ..." — the
	// two-space indent, "--" prefix, placeholder, newline, and
	// four-space-plus-tab continuation mirror stdlib.
	if !strings.Contains(got, "  --columns value\n    \tCOLS (comma-separated)") {
		t.Errorf("long-flag layout missing from output:\n%s", got)
	}
	// Short header stays inline: two-space indent, "-r", tab, usage.
	if !strings.Contains(got, "  -r\trecursive") {
		t.Errorf("short-flag inline layout missing from output:\n%s", got)
	}
}

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
			want:    []string{"--ready", "the ready flag", "Usage: quest t [flags]"},
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
			fs := newFlagSet("t", "[flags]", "Test command.")
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
	fs := newFlagSet("demo", "[flags]", "Demo command.")
	fs.SetOutput(&buf)
	fs.Func("columns", "COLS (comma-separated)", func(string) error { return nil })
	fs.Bool("r", false, "recursive")

	fs.Usage()
	got := buf.String()

	if !strings.HasPrefix(got, "Usage: quest demo [flags]\n") {
		t.Errorf("expected leading \"Usage: quest demo [flags]\" header; got:\n%s", got)
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

// TestUsageRendersSynopsisAndDescription pins STANDARDS.md §Help
// Rendering: the synopsis line names positional/flag args, then a
// blank line, then the one-line description. This is the contract
// `quest tag --help` (and every other flagless command) must satisfy.
func TestUsageRendersSynopsisAndDescription(t *testing.T) {
	var buf bytes.Buffer
	fs := newFlagSet("tag", "ID TAGS",
		"Add tags to a task. Tags are comma-separated, case-insensitive, stored lowercase.")
	fs.SetOutput(&buf)
	fs.Usage()

	want := "Usage: quest tag ID TAGS\n\nAdd tags to a task. Tags are comma-separated, case-insensitive, stored lowercase.\n"
	if buf.String() != want {
		t.Errorf("flagless usage mismatch\n got: %q\nwant: %q", buf.String(), want)
	}
}

// TestUsageOmitsFlagBlockWhenNoFlags pins that flagless commands stop
// after the description — no trailing blank line, no empty flag list.
// Operators reading help on `quest accept` should not see leftover
// whitespace that looks like a missing section.
func TestUsageOmitsFlagBlockWhenNoFlags(t *testing.T) {
	var buf bytes.Buffer
	fs := newFlagSet("accept", "ID",
		"Signal that the agent has received the task and begun work.")
	fs.SetOutput(&buf)
	fs.Usage()

	got := buf.String()
	// Description line is the last non-blank content; output ends with
	// a single newline (from Fprintln on the description), no extra
	// trailing blank line that would imply a missing flag list.
	if !strings.HasSuffix(got, "Signal that the agent has received the task and begun work.\n") {
		t.Errorf("flagless output should end at description; got:\n%s", got)
	}
	if strings.Count(got, "\n\n") != 1 {
		t.Errorf("flagless output should have exactly one blank-line separator (header→description); got:\n%s", got)
	}
}

// TestUsageEmptySynopsisOmitsTrailingSpace pins the version-style case:
// commands with no positional or flag args (only `quest version`) render
// `Usage: quest <name>` with no trailing space or argument placeholder.
func TestUsageEmptySynopsisOmitsTrailingSpace(t *testing.T) {
	var buf bytes.Buffer
	fs := newFlagSet("version", "", "Print version information.")
	fs.SetOutput(&buf)
	fs.Usage()

	got := buf.String()
	if !strings.HasPrefix(got, "Usage: quest version\n") {
		t.Errorf("empty-synopsis header should be %q; got:\n%s", "Usage: quest version", got)
	}
	if strings.HasPrefix(got, "Usage: quest version \n") {
		t.Errorf("empty-synopsis header must not have trailing space; got:\n%s", got)
	}
}

// TestUsageBlankLineBetweenDescriptionAndFlags pins that the flag block
// (when present) is preceded by a blank line. Without the separator the
// flag list runs into the description and is hard to scan.
func TestUsageBlankLineBetweenDescriptionAndFlags(t *testing.T) {
	var buf bytes.Buffer
	fs := newFlagSet("list", "[flags]", "List tasks with filtering.")
	fs.SetOutput(&buf)
	fs.Bool("ready", false, "only ready tasks")
	fs.Usage()

	got := buf.String()
	if !strings.Contains(got, "List tasks with filtering.\n\n  --ready") {
		t.Errorf("expected blank line between description and flag list; got:\n%s", got)
	}
}

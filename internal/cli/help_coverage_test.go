package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestEveryDescriptorHasHelpFlagSet is the coverage contract from the
// 2026-05-06 grove decision (CLI help). Every row in `descriptors` —
// the authoritative dispatch table — MUST expose a non-nil HelpFlagSet
// whose Usage() emits non-empty text starting with the command's
// canonical synopsis line. The roster is derived from the descriptor
// slice, never hand-maintained, so future commands automatically
// inherit the requirement.
//
// `help` itself is the one exception: the help command renders the
// dispatcher's banner (a role-filtered command list) rather than a
// per-command help block, so it carries a nil HelpFlagSet.
// TestHelpCommandHasNilHelpFlagSet pins that carve-out separately.
func TestEveryDescriptorHasHelpFlagSet(t *testing.T) {
	for _, d := range descriptors {
		if d.Name == "help" {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			if d.HelpFlagSet == nil {
				t.Fatalf("%s: HelpFlagSet is nil", d.Name)
			}
			fs := d.HelpFlagSet()
			if fs == nil {
				t.Fatalf("%s: HelpFlagSet returned nil FlagSet", d.Name)
			}
			var buf bytes.Buffer
			fs.SetOutput(&buf)
			fs.Usage()
			if buf.Len() == 0 {
				t.Fatalf("%s: HelpFlagSet().Usage() produced empty output", d.Name)
			}
			want := "Usage: quest " + d.Name
			if !strings.HasPrefix(buf.String(), want) {
				t.Errorf("%s: usage does not start with %q; got:\n%s",
					d.Name, want, buf.String())
			}
		})
	}
}

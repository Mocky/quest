package command

import (
	"flag"
	"fmt"
	"strings"
)

// newFlagSet returns a flag.FlagSet whose Usage method renders multi-
// character flag names with a "--" prefix and single-character names
// with a "-" prefix, matching STANDARDS.md §Help Rendering. Go's stdlib
// flag package prefixes every name with a single dash regardless of
// length, so help output for `--status` would print as `-status` and
// drift from the docs; this wrapper installs a custom Usage function
// that applies the documented convention uniformly.
//
// The returned FlagSet uses ContinueOnError — quest handlers intercept
// flag.ErrHelp and parse failures rather than letting the flag package
// call os.Exit.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { printFlagDefaults(fs) }
	return fs
}

// printFlagDefaults writes the usage block for fs: a "Usage of <name>:"
// header followed by one entry per registered flag. The layout mirrors
// flag.FlagSet.PrintDefaults (two-space indent, tab-then-usage for
// short entries, indented continuation for long entries) so readers
// see a familiar shape; only the dash prefix differs.
func printFlagDefaults(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, "Usage of %s:\n", fs.Name())
	fs.VisitAll(func(fl *flag.Flag) {
		var b strings.Builder
		prefix := "-"
		if len(fl.Name) > 1 {
			prefix = "--"
		}
		fmt.Fprintf(&b, "  %s%s", prefix, fl.Name)
		name, usage := flag.UnquoteUsage(fl)
		if len(name) > 0 {
			b.WriteString(" ")
			b.WriteString(name)
		}
		if b.Len() <= 4 {
			b.WriteString("\t")
		} else {
			b.WriteString("\n    \t")
		}
		b.WriteString(strings.ReplaceAll(usage, "\n", "\n    \t"))
		fmt.Fprintln(out, b.String())
	})
}

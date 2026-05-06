package command

import (
	"flag"
	"fmt"
	"strings"
)

// newFlagSet returns a flag.FlagSet whose Usage method renders the
// three-part help block defined in STANDARDS.md §Help Rendering: a
// `Usage: quest <name> <synopsis>` line, a one-line description, and
// (when flags are registered) a flag list using the long-`--` /
// short-`-` dash convention. Go's stdlib flag package would emit only
// `Usage of <name>:` plus a flag list, which leaves flagless commands
// with empty help and prefixes every long flag with a single dash; the
// custom Usage installed here closes both gaps.
//
// The returned FlagSet uses ContinueOnError — quest handlers translate
// parse failures into ErrUsage rather than letting the flag package
// call os.Exit. Help dispatch is centralized at the dispatcher
// (`quest help <cmd>` per the 2026-05-06 grove decision); handlers no
// longer see `--help` or `-h` because Execute rejects flag-form help
// before the descriptor's handler runs.
func newFlagSet(name, synopsis, description string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { printUsage(fs, synopsis, description) }
	return fs
}

// printUsage writes the help block for fs: the synopsis line, a blank
// line, the description, then (only if at least one flag is registered)
// a blank line followed by the flag list. Each section is required by
// STANDARDS.md §Help Rendering; the blank-line separators are part of
// the contract so operators see consistent spacing across subcommands.
func printUsage(fs *flag.FlagSet, synopsis, description string) {
	out := fs.Output()
	if synopsis == "" {
		fmt.Fprintf(out, "Usage: quest %s\n", fs.Name())
	} else {
		fmt.Fprintf(out, "Usage: quest %s %s\n", fs.Name(), synopsis)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, description)

	hasFlags := false
	fs.VisitAll(func(*flag.Flag) { hasFlags = true })
	if !hasFlags {
		return
	}
	fmt.Fprintln(out)
	printFlagDefaults(fs)
}

// printFlagDefaults writes the per-flag entries for fs. The layout
// mirrors flag.FlagSet.PrintDefaults (two-space indent, tab-then-usage
// for short entries, indented continuation for long entries) so readers
// see a familiar shape; only the dash prefix differs.
func printFlagDefaults(fs *flag.FlagSet) {
	out := fs.Output()
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

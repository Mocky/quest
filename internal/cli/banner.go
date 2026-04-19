package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/mocky/quest/internal/config"
)

// printBanner writes the dispatcher's usage banner for `quest` with no
// subcommand and for `quest --help`. Workers see the worker-role
// inventory plus init+version; planners (any role listed in
// elevated_roles) see every command. Per the M9 decision in the plan,
// this is the one place that leaks planner-command names to workers
// via help — the role-filtered list keeps the help surface aligned
// with what the caller can actually run.
func printBanner(cfg config.Config, w io.Writer) {
	fmt.Fprintln(w, "Usage: quest <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, name := range availableCommands(cfg) {
		fmt.Fprintln(w, "  "+name)
	}
}

// unknownCommandMessage composes the exit-2 body for an unknown token:
// the "did you mean" hint from cli.Suggest (when a close match exists)
// followed by the role-filtered banner. One line per command keeps the
// output grep-friendly for agents parsing usage errors.
func unknownCommandMessage(bad string, cfg config.Config) string {
	valid := availableCommands(cfg)
	var sb strings.Builder
	sb.WriteString("unknown command ")
	sb.WriteString("'")
	sb.WriteString(bad)
	sb.WriteString("'")
	if s := Suggest(bad, valid); s != "" {
		sb.WriteString("; did you mean '")
		sb.WriteString(s)
		sb.WriteString("'?")
	}
	sb.WriteString("\n")
	sb.WriteString("valid commands: ")
	sb.WriteString(strings.Join(valid, ", "))
	return sb.String()
}

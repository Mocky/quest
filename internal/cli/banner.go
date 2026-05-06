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

// detectHelpFlag scans args left-to-right for `--help` or `-h`. If
// found, returns the literal token plus a candidate command name (the
// first non-flag arg if present). Used by Execute to short-circuit
// flag-form help with a "did you mean" redirect at the top of the
// dispatch, before role gate / workspace discovery / store open. The
// canonical form is `quest help <cmd>`; this scan is the only place
// the obsolete forms are accepted, and only to redirect.
func detectHelpFlag(args []string) (token, candidate string, ok bool) {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			token = a
			break
		}
	}
	if token == "" {
		return "", "", false
	}
	for _, a := range args {
		if a == "--help" || a == "-h" {
			continue
		}
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		candidate = a
		break
	}
	return token, candidate, true
}

// helpFlagRejectionMessage builds the two-line redirect body for an
// obsolete flag-form help invocation. Shape matches the typo-suggestion
// pattern documented in lore (`unknown flag` / `Did you mean:`) — one
// grep target across grove tools. When candidate is empty the
// suggestion is `quest help`; when present it is `quest help <cmd>`.
func helpFlagRejectionMessage(token, candidate string) string {
	suggestion := "quest help"
	if candidate != "" {
		suggestion = "quest help " + candidate
	}
	return "unknown flag: " + token + "\nDid you mean: " + suggestion
}

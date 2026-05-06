package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/telemetry"
)

// executeHelp handles the `quest help [<cmd>]` form per the 2026-05-06
// grove decision — `<tool> help <cmd>` is the only documented help
// surface. With no argument it prints the role-filtered banner (same
// shape as `quest` no-arg). With one argument, it resolves the
// descriptor and renders that command's help block on stdout via the
// HelpFlagSet's Usage().
//
// The help dispatch deliberately runs ahead of the role gate, workspace
// discovery, and store open in Execute so `quest help <any>` is safe
// to probe regardless of role or working directory. That property is
// pinned by TestHelpDoesNotEnterRoleGate.
//
// Sub-subcommand help (`quest help <cmd> <subcmd>`) is rejected as a
// usage error today; quest has no sub-subcommands, so the form is
// reserved for when one is introduced.
func executeHelp(ctx context.Context, args []string, cfg config.Config, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printBanner(cfg, stdout)
		return 0
	}
	if len(args) > 1 {
		err := fmt.Errorf("%w: help: at most one command name expected; sub-subcommand help is not supported",
			errors.ErrUsage)
		return telemetry.RecordDispatchError(ctx, err, stderr)
	}
	target := args[0]
	desc, ok := lookupDescriptor(target)
	if !ok || desc.HelpFlagSet == nil {
		err := fmt.Errorf("%w: %s", errors.ErrUsage, helpUnknownTargetMessage(target, cfg))
		return telemetry.RecordDispatchError(ctx, err, stderr)
	}
	fs := desc.HelpFlagSet()
	fs.SetOutput(stdout)
	fs.Usage()
	return 0
}

// helpUnknownTargetMessage builds the body for `quest help <bogus>`.
// Reuses the existing typo-suggestion shape from suggest.Closest so an
// agent that asks for help on a misspelled command lands at the right
// page. The "no help available for" wording deliberately differs from
// the typo path's "unknown command" so the two error origins remain
// distinguishable in stderr scrapes.
func helpUnknownTargetMessage(bad string, cfg config.Config) string {
	valid := availableCommands(cfg)
	msg := fmt.Sprintf("no help available for '%s'", bad)
	if s := Suggest(bad, valid); s != "" {
		msg += fmt.Sprintf("; did you mean '%s'?", s)
	}
	return msg
}

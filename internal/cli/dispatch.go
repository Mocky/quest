package cli

import (
	"context"
	"flag"
	"io"
	"sort"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/store"
)

// Handler is the contract every quest subcommand handler honors.
// Handlers never call os.Exit, never parse global flags, and never read
// env vars. The dispatcher supplies the already-resolved Config, the
// already-wrapped Store (nil for commands with RequiresWorkspace=false),
// and the arg list stripped of the command name. Handlers return the
// wrapped sentinel error; cli.Execute maps it to the stable exit code.
type Handler func(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error

// HelpFlagSet returns an unparsed *flag.FlagSet whose Usage() renders
// the canonical help block for one command. Used exclusively by the
// help dispatcher (`quest help <cmd>`) — never parsed.
type HelpFlagSet func() *flag.FlagSet

// commandDescriptor is the per-command dispatch record. One row per
// entry in the Task 4.2 inventory; role gate / workspace open / span
// suppression / help builder are pure data on this struct so cli.Execute
// reads them off at dispatch time rather than threading them through
// if/else ladders.
type commandDescriptor struct {
	Name              string
	Handler           Handler
	HelpFlagSet       HelpFlagSet
	Elevated          bool
	RequiresWorkspace bool
	SuppressTelemetry bool
}

// descriptors is the authoritative dispatch table. Adding a command
// means adding a row here; TestRoleGateDenials and
// TestEveryDescriptorHasHelpFlagSet iterate this slice. The Help row
// is registered once command.Help is in place (see Execute the help
// case is dispatched after looking up via descriptorIndex).
var descriptors = []commandDescriptor{
	{Name: "version", Handler: command.Version, HelpFlagSet: command.VersionHelp, Elevated: false, RequiresWorkspace: false, SuppressTelemetry: true},
	{Name: "init", Handler: command.Init, HelpFlagSet: command.InitHelp, Elevated: false, RequiresWorkspace: false, SuppressTelemetry: false},
	{Name: "show", Handler: command.Show, HelpFlagSet: command.ShowHelp, Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "accept", Handler: command.Accept, HelpFlagSet: command.AcceptHelp, Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "update", Handler: command.Update, HelpFlagSet: command.UpdateHelp, Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "complete", Handler: command.Complete, HelpFlagSet: command.CompleteHelp, Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "fail", Handler: command.Fail, HelpFlagSet: command.FailHelp, Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "create", Handler: command.Create, HelpFlagSet: command.CreateHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "batch", Handler: command.Batch, HelpFlagSet: command.BatchHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "cancel", Handler: command.Cancel, HelpFlagSet: command.CancelHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "reset", Handler: command.Reset, HelpFlagSet: command.ResetHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "move", Handler: command.Move, HelpFlagSet: command.MoveHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "link", Handler: command.Link, HelpFlagSet: command.LinkHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "unlink", Handler: command.Unlink, HelpFlagSet: command.UnlinkHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "tag", Handler: command.Tag, HelpFlagSet: command.TagHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "untag", Handler: command.Untag, HelpFlagSet: command.UntagHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "deps", Handler: command.Deps, HelpFlagSet: command.DepsHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "list", Handler: command.List, HelpFlagSet: command.ListHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "graph", Handler: command.Graph, HelpFlagSet: command.GraphHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "export", Handler: command.Export, HelpFlagSet: command.ExportHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "backup", Handler: command.Backup, HelpFlagSet: command.BackupHelp, Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	// `help` is dispatched specially in Execute — it never goes through
	// the standard Handler path. The row exists so availableCommands
	// includes it in the banner; HelpFlagSet is intentionally nil
	// (TestEveryDescriptorHasHelpFlagSet carves out this row) because
	// help renders the banner, not a per-command flag block.
	{Name: "help", Handler: nil, HelpFlagSet: nil, Elevated: false, RequiresWorkspace: false, SuppressTelemetry: true},
}

var descriptorIndex = func() map[string]commandDescriptor {
	out := make(map[string]commandDescriptor, len(descriptors))
	for _, d := range descriptors {
		out[d.Name] = d
	}
	return out
}()

// lookupDescriptor fetches the descriptor for name. The bool mirrors
// map-lookup semantics so callers can distinguish "unknown command"
// from "command known but elevated-only".
func lookupDescriptor(name string) (commandDescriptor, bool) {
	d, ok := descriptorIndex[name]
	return d, ok
}

// availableCommands returns the sorted list of command names the caller
// is allowed to invoke given their resolved role. Workers see the
// worker inventory plus `init` and `version` per spec §System & Info
// Commands (a worker outside a workspace needs the `init` hint even
// though init is role-unrestricted in general). Planners see every
// row in the descriptor table.
func availableCommands(cfg config.Config) []string {
	if config.IsElevated(cfg.Agent.Role, cfg.Workspace.ElevatedRoles) {
		out := make([]string, 0, len(descriptors))
		for _, d := range descriptors {
			out = append(out, d.Name)
		}
		sort.Strings(out)
		return out
	}
	worker := []string{}
	for _, d := range descriptors {
		if !d.Elevated {
			worker = append(worker, d.Name)
		}
	}
	sort.Strings(worker)
	return worker
}

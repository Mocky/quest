package cli

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// Handler is the contract every quest subcommand handler honors.
// Handlers never call os.Exit, never parse global flags, and never read
// env vars. The dispatcher supplies the already-resolved Config, the
// already-wrapped Store (nil for commands with RequiresWorkspace=false),
// and the arg list stripped of the command name. Handlers return the
// wrapped sentinel error; cli.Execute maps it to the stable exit code.
type Handler func(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error

// commandDescriptor is the per-command dispatch record. One row per
// entry in the Task 4.2 inventory; role gate / workspace open / span
// suppression are pure data on this struct so cli.Execute reads them
// off at dispatch time rather than threading them through if/else
// ladders.
type commandDescriptor struct {
	Name              string
	Handler           Handler
	Elevated          bool
	RequiresWorkspace bool
	SuppressTelemetry bool
}

// descriptors is the authoritative dispatch table. Adding a command
// means adding a row here; Task 13.1's TestRoleGateDenials iterates
// this slice. Phase 4 ships with real handlers for `version` only;
// every other row points at a notImplemented placeholder that Phase
// 5-11 swaps out for the real `internal/command/<name>.go` handler.
var descriptors = []commandDescriptor{
	{Name: "version", Handler: command.Version, Elevated: false, RequiresWorkspace: false, SuppressTelemetry: true},
	{Name: "init", Handler: command.Init, Elevated: false, RequiresWorkspace: false, SuppressTelemetry: false},
	{Name: "show", Handler: command.Show, Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "accept", Handler: command.Accept, Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "update", Handler: notImplemented("update"), Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "complete", Handler: notImplemented("complete"), Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "fail", Handler: notImplemented("fail"), Elevated: false, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "create", Handler: notImplemented("create"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "batch", Handler: notImplemented("batch"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "cancel", Handler: notImplemented("cancel"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "reset", Handler: notImplemented("reset"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "move", Handler: notImplemented("move"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "link", Handler: notImplemented("link"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "unlink", Handler: notImplemented("unlink"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "tag", Handler: notImplemented("tag"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "untag", Handler: notImplemented("untag"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "deps", Handler: notImplemented("deps"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "list", Handler: notImplemented("list"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "graph", Handler: notImplemented("graph"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
	{Name: "export", Handler: notImplemented("export"), Elevated: true, RequiresWorkspace: true, SuppressTelemetry: false},
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

// notImplemented returns a Handler that wraps ErrGeneral with a
// "not implemented" message. Replaced row-by-row as Phases 5-11
// land the real command handlers. Until then, elevated-role callers
// who bypass the role gate and reach the handler get a stable
// exit-1 stderr instead of a nil-deref panic.
func notImplemented(name string) Handler {
	return func(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		_ = ctx
		_ = cfg
		_ = s
		_ = args
		_ = stdin
		_ = stdout
		_ = stderr
		return fmt.Errorf("%w: command %q is not implemented yet", errors.ErrGeneral, name)
	}
}

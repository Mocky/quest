package cli

import (
	"fmt"
	"strings"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
)

// ParseGlobals extracts --format and --log-level from args in a
// position-independent way and returns the stripped subcommand args.
// A trailing valueless --format or --log-level (e.g. `quest version
// --format`) returns a wrapped ErrUsage so the caller exits 2 with a
// clear "missing value" message rather than misrouting the flag into
// the unknown-command path.
func ParseGlobals(args []string) (config.Flags, []string, error) {
	var flags config.Flags
	remaining := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--format" && i+1 < len(args):
			flags.Format = args[i+1]
			i += 2
		case a == "--format":
			return config.Flags{}, nil, fmt.Errorf("%w: missing value for --format", errors.ErrUsage)
		case strings.HasPrefix(a, "--format="):
			flags.Format = strings.TrimPrefix(a, "--format=")
			i++
		case a == "--log-level" && i+1 < len(args):
			flags.LogLevel = args[i+1]
			i += 2
		case a == "--log-level":
			return config.Flags{}, nil, fmt.Errorf("%w: missing value for --log-level", errors.ErrUsage)
		case strings.HasPrefix(a, "--log-level="):
			flags.LogLevel = strings.TrimPrefix(a, "--log-level=")
			i++
		default:
			remaining = append(remaining, a)
			i++
		}
	}
	return flags, remaining, nil
}

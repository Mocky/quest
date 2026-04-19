package cli

import (
	"strings"

	"github.com/mocky/quest/internal/config"
)

func ParseGlobals(args []string) (config.Flags, []string) {
	var flags config.Flags
	remaining := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--format" && i+1 < len(args):
			flags.Format = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--format="):
			flags.Format = strings.TrimPrefix(a, "--format=")
			i++
		case a == "--log-level" && i+1 < len(args):
			flags.LogLevel = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--log-level="):
			flags.LogLevel = strings.TrimPrefix(a, "--log-level=")
			i++
		default:
			remaining = append(remaining, a)
			i++
		}
	}
	return flags, remaining
}

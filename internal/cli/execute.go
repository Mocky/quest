package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
)

func Execute(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "quest: usage_error: command required")
		fmt.Fprintln(stderr, "quest: exit 2 (usage_error)")
		return 2
	}
	switch args[0] {
	case "version":
		return command.Version(ctx, cfg, args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "quest: usage_error: unknown command %q\n", args[0])
		fmt.Fprintln(stderr, "quest: exit 2 (usage_error)")
		return 2
	}
}

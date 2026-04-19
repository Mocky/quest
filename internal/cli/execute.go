package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/config"
)

func Execute(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = ctx
	_ = cfg
	_ = stdin
	_ = stdout
	if len(args) == 0 {
		fmt.Fprintln(stderr, "quest: usage_error: command required")
		fmt.Fprintln(stderr, "quest: exit 2 (usage_error)")
		return 2
	}
	fmt.Fprintf(stderr, "quest: general_failure: not implemented: %q\n", args[0])
	fmt.Fprintln(stderr, "quest: exit 1 (general_failure)")
	return 1
}

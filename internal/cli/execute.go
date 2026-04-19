package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
)

func Execute(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return emit(stderr, fmt.Errorf("command required: %w", errors.ErrUsage))
	}
	switch args[0] {
	case "version":
		return command.Version(ctx, cfg, args[1:], stdin, stdout, stderr)
	default:
		return emit(stderr, fmt.Errorf("unknown command %q: %w", args[0], errors.ErrUsage))
	}
}

func emit(w io.Writer, err error) int {
	errors.EmitStderr(err, w)
	return errors.ExitCode(err)
}

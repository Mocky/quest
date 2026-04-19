package command

import (
	"context"
	"io"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/store"
)

// Complete transitions a task to `complete`. Supports two paths per
// spec §Status Lifecycle: `accepted → complete` (worker or verifier
// who accepted the task) and `open → complete` (lead direct-close of
// a parent task). The leaf-direct-close rejection inside closeTask
// keeps the open-start path parent-only.
func Complete(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return closeTask(ctx, cfg, s, args, stdin, stdout, stderr, closeComplete)
}

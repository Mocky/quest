package command

import (
	"context"
	"io"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/store"
)

// Fail transitions a task to `failed` from `accepted` only. Spec
// §Status Lifecycle: `open → failed` is not a supported transition —
// a task that never accepted cannot fail (the lead should cancel it
// instead). Unlike complete, fail has no parent direct-close path.
func Fail(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return closeTask(ctx, cfg, s, args, stdin, stdout, stderr, closeFail)
}

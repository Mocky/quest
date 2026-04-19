package command

import (
	"context"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/batch"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/store"
)

// Untag removes tags from a task. Same parsing/validation as Tag; per-
// tag DELETE rows are tallied so history records only the tags that
// actually changed. Ack always emits the full post-state list.
func Untag(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	_ = stderr
	id, raw, err := resolveTagPositional("untag", args)
	if err != nil {
		return err
	}
	tags, err := batch.NormalizeTagList(raw)
	if err != nil {
		return fmt.Errorf("untag: %w", err)
	}
	return tagApply(ctx, cfg, s, stdout, id, tags, store.TxUntag, false)
}

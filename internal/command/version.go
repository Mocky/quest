package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/buildinfo"
	"github.com/mocky/quest/internal/config"
)

func Version(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = ctx
	_ = args
	_ = stdin
	if cfg.Format == "text" {
		fmt.Fprintln(stdout, buildinfo.Version)
		return 0
	}
	enc := json.NewEncoder(stdout)
	payload := struct {
		Version string `json:"version"`
	}{Version: buildinfo.Version}
	if err := enc.Encode(payload); err != nil {
		fmt.Fprintf(stderr, "quest: general_failure: %v\n", err)
		fmt.Fprintln(stderr, "quest: exit 1 (general_failure)")
		return 1
	}
	return 0
}

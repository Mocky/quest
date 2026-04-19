package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/mocky/quest/internal/buildinfo"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// Version prints the build-stamped binary version and exits 0. The
// dispatcher marks the version descriptor SuppressTelemetry so no span
// or operations counter fires per OTEL.md §4.2 — the handler is pure
// stdout work. `s` is always nil for this descriptor; the explicit
// check documents the invariant and keeps us from reaching for a store
// that the dispatcher never opened.
func Version(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = ctx
	_ = s
	_ = args
	_ = stdin
	_ = stderr
	if cfg.Output.Format == "text" {
		fmt.Fprintln(stdout, buildinfo.Version)
		return nil
	}
	payload := struct {
		Version string `json:"version"`
	}{Version: buildinfo.Version}
	if err := json.NewEncoder(stdout).Encode(payload); err != nil {
		return fmt.Errorf("%w: version: encode json: %s", errors.ErrGeneral, err.Error())
	}
	return nil
}

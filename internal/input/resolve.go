package input

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mocky/quest/internal/errors"
)

// MaxBytes is the 1 MiB cap applied to every @file / @- argument per
// spec §Input Conventions. Values at or below the cap pass through
// verbatim; values exceeding the cap are rejected with exit 2 and a
// source-aware stderr message naming the flag and the observed size.
const MaxBytes = 1 * 1024 * 1024

// Resolver expands @file / @- / bare-string arguments per the input
// convention. Construct one per invocation: the tracker field carries
// per-invocation state (which flag consumed stdin) so a second @-
// argument on the same command is rejected with exit 2 and a pointer
// at the first flag. Stdin is a single byte stream; consuming it twice
// would silently corrupt the second read.
type Resolver struct {
	stdin   io.Reader
	stdinBy string // flag name that already consumed @-
}

// NewResolver returns a Resolver bound to the supplied stdin. Handlers
// call it once at entry and use the same instance for every free-form
// flag on that invocation so the @- single-use rule is enforced across
// flags.
func NewResolver(stdin io.Reader) *Resolver {
	return &Resolver{stdin: stdin}
}

// Resolve expands val per the @file convention: @- reads stdin, @path
// reads the file (path resolved against the caller's CWD), anything
// else passes through unchanged. flag is the user-facing flag name
// (e.g., "--debrief"); it is the first token in every error message so
// agents can route programmatically. Sole carve-out: the second-@-
// rejection message in resolveStdin omits the flag prefix because
// spec §Input Conventions pins its wording verbatim with the first
// consumer's flag name in lead position.
func (r *Resolver) Resolve(flag, val string) (string, error) {
	if !strings.HasPrefix(val, "@") {
		return val, nil
	}
	arg := val[1:]
	if arg == "-" {
		return r.resolveStdin(flag)
	}
	return r.resolveFile(flag, arg)
}

// resolveStdin reads up to MaxBytes+1 from r.stdin — the extra byte
// lets us detect oversized inputs without buffering the whole stream.
// Second @- on the same invocation rejects with the spec-pinned
// wording (§Input Conventions): the message leads with the first
// consumer's flag, not the current flag, so prefix-anchor substring
// matching against the spec example works verbatim.
func (r *Resolver) resolveStdin(flag string) (string, error) {
	if r.stdinBy != "" {
		return "", fmt.Errorf("stdin already consumed by %s; at most one @- per invocation: %w",
			r.stdinBy, errors.ErrUsage)
	}
	r.stdinBy = flag
	b, err := io.ReadAll(io.LimitReader(r.stdin, MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("%s: failed to read @-: %s: %w", flag, err.Error(), errors.ErrUsage)
	}
	if len(b) > MaxBytes {
		return "", fmt.Errorf("%s: stdin exceeds 1 MiB limit (observed %d bytes): %w",
			flag, len(b), errors.ErrUsage)
	}
	return string(b), nil
}

// resolveFile reads @path (relative to CWD). Missing file / permission
// denied / read errors all map to exit 2 with the underlying OS error
// appended; oversized files are rejected before Read to avoid touching
// gigabyte-sized log files that might be sitting behind a misaimed
// flag.
func (r *Resolver) resolveFile(flag, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s: failed to read @%s: %s: %w", flag, path, err.Error(), errors.ErrUsage)
	}
	if info.Size() > MaxBytes {
		return "", fmt.Errorf("%s: file @%s exceeds 1 MiB limit (observed %d bytes): %w",
			flag, path, info.Size(), errors.ErrUsage)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: failed to read @%s: %s: %w", flag, path, err.Error(), errors.ErrUsage)
	}
	return string(b), nil
}

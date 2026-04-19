package errors

import (
	stderrors "errors"
	"fmt"
	"io"
	"strings"
)

// Sentinel errors — every quest exit path wraps one of these so
// errors.Is (the stdlib one) can walk the chain and map the error to
// the right exit code and class. The wire contract is: exit codes 1-7
// are stable (quest-spec.md §Exit codes); class strings are the
// quest.error.class vocabulary on OTEL spans (OTEL.md §4.4).
var (
	ErrGeneral    = stderrors.New("general failure")
	ErrUsage      = stderrors.New("usage error")
	ErrNotFound   = stderrors.New("not found")
	ErrPermission = stderrors.New("permission denied")
	ErrConflict   = stderrors.New("conflict")
	ErrRoleDenied = stderrors.New("role denied")
	ErrTransient  = stderrors.New("transient failure")
)

type classInfo struct {
	sentinel  error
	exit      int
	class     string
	retryable bool
}

// classes is the single source of truth for the exit / class / retryable
// triple. TestExitCodeStability in errors_test.go pins the rows; touching
// this table is a wire-contract change that must ship with a spec update
// and a CHANGELOG entry per STANDARDS.md §Exit Code Stability.
var classes = []classInfo{
	{ErrGeneral, 1, "general_failure", false},
	{ErrUsage, 2, "usage_error", false},
	{ErrNotFound, 3, "not_found", false},
	{ErrPermission, 4, "permission_denied", false},
	{ErrConflict, 5, "conflict", false},
	{ErrRoleDenied, 6, "role_denied", false},
	{ErrTransient, 7, "transient_failure", true},
}

// ExitCode returns the stable process exit code for err. Nil → 0.
// Unknown errors → 1 (general_failure), per spec.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if c, ok := lookup(err); ok {
		return c.exit
	}
	return 1
}

// Class returns the OTEL quest.error.class vocabulary string matching
// err's sentinel. Nil → "". Unknown errors → "general_failure".
func Class(err error) string {
	if err == nil {
		return ""
	}
	if c, ok := lookup(err); ok {
		return c.class
	}
	return "general_failure"
}

// Retryable reports whether err is wrapped around ErrTransient. Only
// exit 7 is retryable per spec.
func Retryable(err error) bool {
	return stderrors.Is(err, ErrTransient)
}

// UserMessage returns a sanitized one-liner for the `quest: <class>:
// <message>` stderr line. It strips the sentinel's bare text from the
// tail of the chain so the user sees the caller's context string
// rather than the sentinel's placeholder words. Nothing leaks from
// wrapped internals beyond the caller's own wrapping — callers must
// keep %w chains free of SQL/paths/types per OBSERVABILITY.md
// §Sanitization.
func UserMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if c, ok := lookup(err); ok {
		if tail := ": " + c.sentinel.Error(); strings.HasSuffix(msg, tail) {
			msg = strings.TrimSuffix(msg, tail)
		} else if msg == c.sentinel.Error() {
			msg = c.class
		}
	}
	return msg
}

// EmitStderr writes the canonical two-line error tail defined by
// cross-cutting.md §Error messages: the `quest: <class>: <actionable
// message>` line followed by `quest: exit N (<class>)`. This is the
// only place that formats that tail — handlers and the dispatcher
// route every non-zero exit path through here (via
// telemetry.RecordDispatchError once the dispatcher lands) so the
// contract stays in one spot.
func EmitStderr(err error, w io.Writer) {
	if err == nil {
		return
	}
	class := Class(err)
	fmt.Fprintf(w, "quest: %s: %s\n", class, UserMessage(err))
	fmt.Fprintf(w, "quest: exit %d (%s)\n", ExitCode(err), class)
}

func lookup(err error) (classInfo, bool) {
	for _, c := range classes {
		if stderrors.Is(err, c.sentinel) {
			return c, true
		}
	}
	return classInfo{}, false
}

// NewSchemaTooNew produces the spec-pinned error returned when the
// stored schema version exceeds the one this binary supports. Wording
// is verbatim from quest-spec.md §Storage so the dispatcher
// (Task 4.2 step 5) and quest init handler emit identical stderr text;
// exposing the constructor here keeps the single source of truth for
// an error string that agents may switch on.
func NewSchemaTooNew(stored, supported int) error {
	return fmt.Errorf("%w: database schema version %d is newer than this binary supports -- upgrade quest (binary supports %d)", ErrGeneral, stored, supported)
}

// NewTransient produces the spec-pinned error returned when the write
// lock cannot be acquired within PRAGMA busy_timeout. Wording of the
// leading phrase is verbatim from quest-spec.md §Storage so agents
// switching on the stderr line see the same string across releases;
// driverMsg is the underlying sqlite error text, appended in parens
// for operator-side debugging without polluting the pinned prefix.
func NewTransient(driverMsg string) error {
	return fmt.Errorf("write lock unavailable after 5s -- transient failure, safe to retry (%s): %w", driverMsg, ErrTransient)
}

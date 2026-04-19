package errors_test

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"testing"

	qerrors "github.com/mocky/quest/internal/errors"
)

// TestExitCodeStability pins the exit-code-to-class mapping per
// STANDARDS.md §Exit Code Stability. Failures here are a wire-contract
// regression — fix the code, not the test.
func TestExitCodeStability(t *testing.T) {
	cases := []struct {
		sentinel error
		code     int
		class    string
	}{
		{qerrors.ErrGeneral, 1, "general_failure"},
		{qerrors.ErrUsage, 2, "usage_error"},
		{qerrors.ErrNotFound, 3, "not_found"},
		{qerrors.ErrPermission, 4, "permission_denied"},
		{qerrors.ErrConflict, 5, "conflict"},
		{qerrors.ErrRoleDenied, 6, "role_denied"},
		{qerrors.ErrTransient, 7, "transient_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			if got := qerrors.ExitCode(tc.sentinel); got != tc.code {
				t.Errorf("ExitCode = %d; want %d", got, tc.code)
			}
			if got := qerrors.Class(tc.sentinel); got != tc.class {
				t.Errorf("Class = %q; want %q", got, tc.class)
			}
		})
	}
}

func TestExitCodeOnNilIsZero(t *testing.T) {
	if got := qerrors.ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d; want 0", got)
	}
	if got := qerrors.Class(nil); got != "" {
		t.Errorf("Class(nil) = %q; want empty", got)
	}
}

func TestExitCodeWrappedPreservesMapping(t *testing.T) {
	err := fmt.Errorf("accept proj-a1: %w", qerrors.ErrConflict)
	if got := qerrors.ExitCode(err); got != 5 {
		t.Errorf("ExitCode(wrapped conflict) = %d; want 5", got)
	}
	if got := qerrors.Class(err); got != "conflict" {
		t.Errorf("Class(wrapped conflict) = %q; want conflict", got)
	}
}

func TestExitCodeUnknownErrIsOne(t *testing.T) {
	err := stderrors.New("something else")
	if got := qerrors.ExitCode(err); got != 1 {
		t.Errorf("ExitCode(unknown) = %d; want 1", got)
	}
	if got := qerrors.Class(err); got != "general_failure" {
		t.Errorf("Class(unknown) = %q; want general_failure", got)
	}
}

func TestRetryableOnlyTransient(t *testing.T) {
	if !qerrors.Retryable(qerrors.ErrTransient) {
		t.Error("ErrTransient should be retryable")
	}
	if qerrors.Retryable(qerrors.ErrConflict) {
		t.Error("ErrConflict must not be retryable")
	}
	if qerrors.Retryable(nil) {
		t.Error("nil must not be retryable")
	}
}

func TestUserMessageStripsSentinelTail(t *testing.T) {
	err := fmt.Errorf("proj-a1 is already accepted: %w", qerrors.ErrConflict)
	if got := qerrors.UserMessage(err); got != "proj-a1 is already accepted" {
		t.Errorf("UserMessage = %q; want %q", got, "proj-a1 is already accepted")
	}
}

func TestUserMessageBareSentinelFallsBackToClass(t *testing.T) {
	if got := qerrors.UserMessage(qerrors.ErrNotFound); got != "not_found" {
		t.Errorf("UserMessage(bare ErrNotFound) = %q; want class string", got)
	}
}

func TestEmitStderrShape(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("proj-a1 has non-terminal children: %w", qerrors.ErrConflict)
	qerrors.EmitStderr(err, &buf)
	want := "quest: conflict: proj-a1 has non-terminal children\nquest: exit 5 (conflict)\n"
	if got := buf.String(); got != want {
		t.Errorf("EmitStderr output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestEmitStderrNilIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	qerrors.EmitStderr(nil, &buf)
	if buf.Len() != 0 {
		t.Errorf("EmitStderr(nil) wrote %q", buf.String())
	}
}

// TestTransientFailureStderrPinning pins quest-spec.md §Storage: the
// exit-7 stderr line must lead with the verbatim phrase
// "write lock unavailable after 5s -- transient failure, safe to retry"
// so agents can switch on it. Driver detail is appended in parens for
// operator-side debugging. Failures here are a wire-contract regression.
func TestTransientFailureStderrPinning(t *testing.T) {
	var buf bytes.Buffer
	err := qerrors.NewTransient("SQLITE_BUSY: database is locked")
	qerrors.EmitStderr(err, &buf)
	want := "quest: transient_failure: write lock unavailable after 5s -- transient failure, safe to retry (SQLITE_BUSY: database is locked)\nquest: exit 7 (transient_failure)\n"
	if got := buf.String(); got != want {
		t.Errorf("EmitStderr output mismatch\n got: %q\nwant: %q", got, want)
	}
	if !stderrors.Is(err, qerrors.ErrTransient) {
		t.Error("NewTransient must wrap ErrTransient so Retryable and ExitCode see it")
	}
}

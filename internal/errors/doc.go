// Package errors owns quest's exit-code and error-class mapping. It
// exposes typed sentinels (ErrGeneral, ErrUsage, ErrNotFound,
// ErrPermission, ErrConflict, ErrRoleDenied, ErrTransient), ExitCode
// and Class walkers that use stdlib errors.Is through wrapped chains,
// Retryable for exit-7 detection, UserMessage for the sanitized
// stderr one-liner, and EmitStderr as the sole formatter of the
// canonical two-line tail defined in cross-cutting.md §Error messages.
// The exit codes 1–7 and class strings are pinned by
// TestExitCodeStability per STANDARDS.md §CLI Output Contract Tests —
// renumbering is a breaking change that must ship with a spec update.
// Only main, cli.Execute, and telemetry.RecordDispatchError translate
// errors to os.Exit codes; handlers return wrapped sentinels.
package errors

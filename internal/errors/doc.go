// Package errors owns quest's exit-code and error-class mapping. The
// spec pins exit codes 1–7 to stable error classes (`general_failure`,
// `usage_error`, `not_found`, `permission_denied`, `conflict`,
// `role_denied`, `transient_failure`); changes ripple through the OTEL
// `quest.error.class` attribute contract and require a spec update.
// Planned exports: exit-code constants, error sentinels, ExitCode(err)
// int, and Class(err) string. Only main and cli.Execute translate errors
// into os.Exit codes — handlers return typed errors. Phase 2 fills this
// in. See quest-spec.md §Output & Error Conventions and
// OBSERVABILITY.md §Error Handling.
package errors

// Package buildinfo holds the single build-time version string. Version
// is set via `-ldflags "-X github.com/mocky/quest/internal/buildinfo.Version=..."`
// from the Makefile's build target and defaults to "dev" for untagged
// local builds. Importers are cmd/quest/main.go (for telemetry service
// version) and internal/command/version.go (for `quest version` output).
// No internal dependencies; no OTEL imports. See OTEL.md §4.2 and
// quest-spec.md §`quest version`.
package buildinfo

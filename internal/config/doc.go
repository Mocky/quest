// Package config is the single reader of environment variables, CLI
// flags, and .quest/config.toml (STANDARDS.md Part 1). Every other
// package accepts resolved config values as parameters — no os.Getenv,
// no flag.Parse, no TOML reads elsewhere. Exported types: Flags (the
// two-field global-flag value cli.ParseGlobals produces), Config (the
// resolved values main.run() hands to cli.Execute), and the typed
// Log/Agent/Telemetry sub-structs. Phase 1 fills in workspace discovery,
// TOML parsing, and Validate; Phase 0 provides the minimum surface the
// startup flow needs. See STANDARDS.md §Configuration Management.
package config

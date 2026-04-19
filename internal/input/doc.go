// Package input resolves the `@file` / `@-` / bare-string input
// convention from quest-spec.md §Input Conventions. Handlers construct
// a Resolver per invocation (r := input.NewResolver(stdin)) and call
// r.Resolve(flagName, raw) for every free-form flag listed in the spec
// (`--debrief`, `--note`, `--handoff`, `--description`, `--context`,
// `--reason`, `--acceptance-criteria`). The Resolver carries
// per-invocation state so the second `@-` on one call rejects with
// exit 2 and a pointer to the flag that already consumed stdin
// (cross-cutting.md §`@file` input). Phase 6 lands the implementation
// alongside worker-command handlers.
package input

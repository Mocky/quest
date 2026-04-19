package buildinfo

// Version is injected at build time by the Makefile's build target using
// -ldflags -X. The literal "dev" default covers direct `go build` and
// `go run` invocations where the ldflag is absent so the `quest version`
// output contract (quest-spec.md §`quest version`: version is always a
// non-empty string) holds for every build path.
var Version = "dev"

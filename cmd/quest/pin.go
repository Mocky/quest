package main

// Pin dependency versions referenced by the Task 0.1 plan but not yet
// imported by the packages that will use them (toml → internal/config in
// Phase 1, sqlite → internal/store in Phase 3, term → internal/output in
// Phase 4). These blank imports keep the modules in go.mod through
// `go mod tidy` until the real imports arrive and this file is removed.
import (
	_ "github.com/BurntSushi/toml"
	_ "golang.org/x/term"
	_ "modernc.org/sqlite"
)

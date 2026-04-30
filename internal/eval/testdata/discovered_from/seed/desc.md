`ParseDate` in `foo.go` is broken. It is supposed to parse `YYYY-MM-DD` strings (e.g. `2026-04-30`) but currently uses a `MM/DD/YYYY` format and fails on every valid input.

Fix the format string in `ParseDate` so it correctly parses `YYYY-MM-DD` inputs.

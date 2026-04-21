// Package store owns quest's SQLite layer: connection pool, migrations,
// and CRUD for tasks / history / deps / tags / notes. Writes are
// serialized through BeginImmediate, which maps lock contention to
// exit code 7 per quest-spec.md §Storage. Reads never use ReadOnly:
// true transactions (modernc.org/sqlite silently downgrades BEGIN
// IMMEDIATE for read-only, which would bypass the write-lock contract).
// The connect hook sets journal_mode=WAL / busy_timeout=5000 /
// foreign_keys=ON. See quest-spec.md §Storage and §History field.
package store

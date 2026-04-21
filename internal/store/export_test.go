package store

// MigrateFromFS exposes the internal migrate(ctx, s, src) worker so
// the integration test for forward-only-never-partial rollback can
// ship a poisoned migration set without mutating the package-scope
// embed.FS.
var MigrateFromFS = migrate

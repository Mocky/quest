package store

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"strconv"

	"github.com/mocky/quest/internal/errors"
)

// CurrentSchemaVersion reports the schema version recorded in the meta
// table. A fresh database has no meta table yet — return 0 (not an
// error) so the dispatcher can feed the value straight into
// telemetry.MigrateSpan(ctx, from, to) without special-casing the
// bootstrap path. Task 3.2's migration 001 is responsible for creating
// the meta table and setting schema_version = 1.
//
// Error mapping: SQLITE_BUSY wraps ErrTransient (exit 7); any other
// driver error wraps ErrGeneral (exit 1). The "meta missing" sentinel
// is the only non-error zero return.
func (s *sqliteStore) CurrentSchemaVersion(ctx context.Context) (int, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&name)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, classifyDriverErr(err)
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&raw); err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, classifyDriverErr(err)
	}
	v, perr := strconv.Atoi(raw)
	if perr != nil {
		return 0, fmt.Errorf("%w: meta.schema_version: %q is not an integer", errors.ErrGeneral, raw)
	}
	return v, nil
}

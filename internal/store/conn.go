package store

import (
	"context"

	"modernc.org/sqlite"
)

// init registers the per-connection pragma hook once at package load
// time. Installing the hook from store.Open would re-register on every
// call (harmless but a slow leak in tests that open many DBs); a
// package-level init guarantees exactly-once registration.
// busy_timeout and foreign_keys are per-connection pragmas that
// default to 0 / OFF on every fresh connection Go's pool may open, so
// the hook must apply them before the DB returns the connection to the
// pool.
func init() {
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=5000", nil); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON", nil); err != nil {
			return err
		}
		return nil
	})
}

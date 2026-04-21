package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"

	"github.com/mocky/quest/internal/errors"
)

// backupCapable is a structural interface Go matches against the
// unexported modernc.org/sqlite *conn type. NewBackup is exported on
// *conn; only the receiver type is private, so shape-matching via
// sql.Conn.Raw is the supported escape hatch for reaching the online
// backup API. If the driver upgrades and renames the method this
// match fails at runtime — TestBackupCapableInterfaceAssertion pins
// the shape so the breakage surfaces in CI rather than at 3 a.m. in
// a pre-migration snapshot abort.
type backupCapable interface {
	NewBackup(dstUri string) (*sqlite.Backup, error)
}

// Snapshot copies the live database to dstPath via SQLite's online
// backup API. The copy is transaction-consistent and does not require
// quiescing the workspace — agents may keep operating on the DB while
// the snapshot runs. Returns the byte size of the produced file.
//
// Implementation: pin one pool connection via sql.DB.Conn, cast it to
// backupCapable via conn.Raw, call NewBackup(dstPath), then Step(-1)
// until the copy is complete. Step(-1) copies all remaining pages in
// one go; per-batch yielding is unnecessary for a one-shot snapshot.
//
// Any pre-existing file at dstPath is removed first so the backup API
// always writes into a fresh SQLite DB — passing it an existing non-
// SQLite file fails with "file is not a database", and retrying after
// a half-written snapshot would otherwise inherit the broken file.
// This matches the spec idempotency note: "If the file already
// exists, it is overwritten."
func (s *sqliteStore) Snapshot(ctx context.Context, dstPath string) (int64, error) {
	if err := os.Remove(dstPath); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("%w: %s", errors.ErrGeneral, err.Error())
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, classifyDriverErr(err)
	}
	defer conn.Close()

	rawErr := conn.Raw(func(driverConn any) error {
		bc, ok := driverConn.(backupCapable)
		if !ok {
			return fmt.Errorf("%w: sqlite driver does not expose NewBackup", errors.ErrGeneral)
		}
		b, bErr := bc.NewBackup(dstPath)
		if bErr != nil {
			return bErr
		}
		for {
			more, sErr := b.Step(-1)
			if sErr != nil {
				if fErr := b.Finish(); fErr != nil {
					slog.WarnContext(ctx, "snapshot finish after step failure",
						"path", dstPath,
						"err", fErr.Error(),
					)
				}
				return sErr
			}
			if !more {
				break
			}
		}
		return b.Finish()
	})
	if rawErr != nil {
		return 0, classifyDriverErr(rawErr)
	}
	fi, statErr := os.Stat(dstPath)
	if statErr != nil {
		return 0, fmt.Errorf("%w: %s", errors.ErrGeneral, statErr.Error())
	}
	return fi.Size(), nil
}

// PreMigrationSnapshot writes a timestamped backup of the DB to
// .quest/backups/pre-v{target}-{timestamp}.db before migrations run.
// Callers gate on from > 0 and from < SupportedSchemaVersion; this
// helper does not re-check. Returns the target path so the caller can
// surface it in error messages per spec §Pre-migration snapshot.
//
// Same-second retry overwrites the prior file. The only path that can
// hit that collision is "migration snapshot fails, operator
// immediately re-runs" — overwriting a failed-mid-write file with a
// fresh one is the correct outcome.
func PreMigrationSnapshot(ctx context.Context, workspaceRoot string, s Store, target int) (string, error) {
	ts := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(workspaceRoot, ".quest", "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("%w: create backups dir: %s", errors.ErrGeneral, err.Error())
	}
	path := filepath.Join(dir, fmt.Sprintf("pre-v%d-%s.db", target, ts))
	if _, err := s.Snapshot(ctx, path); err != nil {
		return path, err
	}
	return path, nil
}

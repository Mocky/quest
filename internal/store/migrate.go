package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/mocky/quest/internal/errors"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// SupportedSchemaVersion is the highest schema version this binary
// can operate against. Bumped by every migration that lands.
// Task 3.2's TestMigrationSequenceContiguous asserts the highest
// numeric migration prefix equals this constant.
const SupportedSchemaVersion = 3

// Migrate applies every pending SQL migration in a single transaction
// and returns the count of migration files actually executed. Callers
// (the dispatcher's Task 4.2 step 5, quest init's handler) invoke
// Migrate after Open — Open itself is cheap and span-free so the
// migrate span stays a sibling of the command span per OTEL.md §8.8.
//
// If the stored version exceeds SupportedSchemaVersion, Migrate
// returns the NewSchemaTooNew error and leaves the DB untouched.
// On any migration-SQL failure the transaction rolls back and the DB
// remains at the prior version — spec §Storage pins this
// "forward-only, never partial" behavior.
// unwrapper lets a decorator surface the inner Store so Migrate's
// type assertion still works after telemetry.WrapStore. Decorators
// implement Unwrap() Store; the bare store does not need to.
type unwrapper interface{ Unwrap() Store }

func Migrate(ctx context.Context, s Store) (int, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return 0, fmt.Errorf("%w: sub migrations: %s", errors.ErrGeneral, err.Error())
	}
	return migrate(ctx, s, sub)
}

// migrate is the workhorse that Migrate (production) and tests drive.
// Accepting an fs.FS lets the migration-failure-rolls-back test ship a
// poisoned migration set without mutating the embedded FS at package
// scope.
func migrate(ctx context.Context, s Store, src fs.FS) (int, error) {
	for {
		if u, ok := s.(unwrapper); ok {
			s = u.Unwrap()
			continue
		}
		break
	}
	impl, ok := s.(*sqliteStore)
	if !ok {
		return 0, fmt.Errorf("%w: store.Migrate called with non-sqlite store", errors.ErrGeneral)
	}
	files, err := loadMigrationsFromFS(src)
	if err != nil {
		return 0, err
	}
	stored, err := impl.CurrentSchemaVersion(ctx)
	if err != nil {
		return 0, err
	}
	head := files[len(files)-1].version
	switch {
	case stored > head:
		return 0, errors.NewSchemaTooNew(stored, head)
	case stored == head:
		return 0, nil
	}

	// Migrations that recreate tables to add CHECK constraints (see
	// 003) follow the SQLite-documented CREATE/INSERT/DROP/RENAME
	// pattern, which leaves FK references transiently pointing at the
	// dropped-then-renamed table. The commit-time FK check fires on
	// those dangling bindings even with PRAGMA defer_foreign_keys=ON,
	// so the recommended 12-step procedure disables foreign_keys at the
	// connection level before the transaction opens (inside a tx the
	// PRAGMA is a no-op per SQLite docs). Acquire a dedicated connection
	// so the PRAGMA toggle is scoped to migration work and does not leak
	// to other pool users, and restore the connection to foreign_keys=ON
	// before releasing it so conn.go's per-connection hook invariant
	// still holds for subsequent callers.
	conn, err := impl.db.Conn(ctx)
	if err != nil {
		return 0, classifyDriverErr(err)
	}
	defer func() {
		// Best-effort restore: if the PRAGMA fails we still want to
		// release the connection rather than leak it, and the
		// connection hook will re-apply foreign_keys=ON on the next
		// fresh connection the pool opens.
		_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return 0, classifyDriverErr(err)
	}

	// BeginTx with nil options issues BEGIN IMMEDIATE due to the
	// DSN's _txlock=immediate; migrations therefore already hold the
	// write lock without going through BeginImmediate (which would
	// otherwise tag this as quest.store.tx{tx_kind=...} — see Task
	// 3.2's rationale for excluding migrations from that histogram).
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, classifyDriverErr(err)
	}
	applied := 0
	for _, m := range files {
		if m.version <= stored {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				// Both the migration SQL and the rollback failed -- the DB
				// may be in a partially-migrated state, violating the
				// spec's forward-only-never-partial promise. Point the
				// operator at the pre-migration snapshot so the prior-
				// version file is the recovery path (spec §Storage >
				// Pre-migration snapshot).
				slog.ErrorContext(ctx, "migration rollback failed",
					"schema.from", stored,
					"schema.to", head,
					"migration", fmt.Sprintf("%03d_%s", m.version, m.label),
					"err", rbErr.Error(),
				)
				return 0, fmt.Errorf("%w: migration %03d %s failed and rollback also failed -- database may be partially migrated, restore from .quest/backups/pre-v%d-*.db: exec: %s; rollback: %s",
					errors.ErrGeneral, m.version, m.label, head, err.Error(), rbErr.Error())
			}
			return 0, fmt.Errorf("%w: migration %03d %s: %s", errors.ErrGeneral, m.version, m.label, err.Error())
		}
		applied++
	}
	// Before commit, audit FK integrity. Migrations were run with FK
	// enforcement off so the table-recreation pattern could leave
	// references dangling mid-migration; foreign_key_check runs the full
	// audit regardless of the foreign_keys PRAGMA and surfaces any
	// row that violates a declared FK. Any violation rolls the
	// transaction back to preserve the forward-only-never-partial
	// contract.
	if violations, err := collectFKViolations(ctx, tx); err != nil {
		_ = tx.Rollback()
		return 0, err
	} else if len(violations) > 0 {
		_ = tx.Rollback()
		return 0, fmt.Errorf("%w: migration left foreign key violations: %s", errors.ErrGeneral, strings.Join(violations, "; "))
	}
	if err := tx.Commit(); err != nil {
		return 0, classifyDriverErr(err)
	}
	slog.InfoContext(ctx, "schema migration applied",
		"schema.from", stored,
		"schema.to", head,
		"applied_count", applied,
	)
	return applied, nil
}

// collectFKViolations runs PRAGMA foreign_key_check inside tx and
// returns one descriptive string per violating row. The PRAGMA reports
// columns (table, rowid, parent, fkid); the first three are enough to
// locate the offending row for diagnosis. A driver error from the
// PRAGMA itself (rather than a violation) is returned as the error
// return so callers can distinguish "integrity broken" from "couldn't
// check integrity."
func collectFKViolations(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, fmt.Errorf("%w: foreign_key_check query: %s", errors.ErrGeneral, err.Error())
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var table, rowID, parent, fkID sql.NullString
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return nil, fmt.Errorf("%w: foreign_key_check scan: %s", errors.ErrGeneral, err.Error())
		}
		out = append(out, fmt.Sprintf("%s(rowid=%s) -> %s", table.String, rowID.String, parent.String))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: foreign_key_check rows: %s", errors.ErrGeneral, err.Error())
	}
	return out, nil
}

type migration struct {
	version int
	label   string
	sql     string
}

func loadMigrations() ([]migration, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("%w: sub migrations: %s", errors.ErrGeneral, err.Error())
	}
	out, err := loadMigrationsFromFS(sub)
	if err != nil {
		return nil, err
	}
	// Production-only head invariant: the embedded set must top out at
	// SupportedSchemaVersion so a binary cannot ship believing it knows
	// about a schema it does not actually carry migrations for. Test
	// fixtures use loadMigrationsFromFS directly to skip this check.
	if top := out[len(out)-1].version; top != SupportedSchemaVersion {
		return nil, fmt.Errorf("%w: highest migration version %d does not match SupportedSchemaVersion %d", errors.ErrGeneral, top, SupportedSchemaVersion)
	}
	return out, nil
}

func loadMigrationsFromFS(src fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		return nil, fmt.Errorf("%w: read migrations: %s", errors.ErrGeneral, err.Error())
	}
	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".sql")
		prefix, label, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("%w: migration %q missing NNN_ prefix", errors.ErrGeneral, e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("%w: migration %q has non-integer prefix %q", errors.ErrGeneral, e.Name(), prefix)
		}
		body, err := fs.ReadFile(src, e.Name())
		if err != nil {
			return nil, fmt.Errorf("%w: read migration %q: %s", errors.ErrGeneral, e.Name(), err.Error())
		}
		out = append(out, migration{version: v, label: label, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no migrations embedded", errors.ErrGeneral)
	}
	// Gap invariant: versions must start at 1 and be contiguous.
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("%w: migration gap — expected version %d at index %d, got %d", errors.ErrGeneral, i+1, i, m.version)
		}
	}
	return out, nil
}

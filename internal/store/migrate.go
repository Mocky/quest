package store

import (
	"context"
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
const SupportedSchemaVersion = 2

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

	// BeginTx with nil options issues BEGIN IMMEDIATE due to the
	// DSN's _txlock=immediate; migrations therefore already hold the
	// write lock without going through BeginImmediate (which would
	// otherwise tag this as quest.store.tx{tx_kind=...} — see Task
	// 3.2's rationale for excluding migrations from that histogram).
	tx, err := impl.db.BeginTx(ctx, nil)
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

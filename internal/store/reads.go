package store

import (
	"context"
	"fmt"

	"github.com/mocky/quest/internal/errors"
)

// Task 3.3 lands stub method bodies for every Store read method. Each
// returns a wrapped ErrGeneral("not implemented") until the matching
// command handler fills it in alongside its own SQL in Phase 5+. The
// stubs exist so the whole interface compiles today — the dispatcher
// (Task 4.2), the testutil fake (Task 0.1 inventory), and the
// decorator (Task 12.4) all depend on the method set being stable.

func notImplemented(method string) error {
	return fmt.Errorf("%w: store.%s not implemented", errors.ErrGeneral, method)
}

func (s *sqliteStore) GetTask(ctx context.Context, id string) (Task, error) {
	_ = ctx
	_ = id
	return Task{}, notImplemented("GetTask")
}

func (s *sqliteStore) GetTaskWithDeps(ctx context.Context, id string) (Task, error) {
	_ = ctx
	_ = id
	return Task{}, notImplemented("GetTaskWithDeps")
}

func (s *sqliteStore) ListTasks(ctx context.Context, filter Filter) ([]Task, error) {
	_ = ctx
	_ = filter
	return nil, notImplemented("ListTasks")
}

func (s *sqliteStore) GetHistory(ctx context.Context, id string) ([]History, error) {
	_ = ctx
	_ = id
	return nil, notImplemented("GetHistory")
}

func (s *sqliteStore) GetChildren(ctx context.Context, parentID string) ([]Task, error) {
	_ = ctx
	_ = parentID
	return nil, notImplemented("GetChildren")
}

func (s *sqliteStore) GetDependencies(ctx context.Context, id string) ([]Dependency, error) {
	_ = ctx
	_ = id
	return nil, notImplemented("GetDependencies")
}

func (s *sqliteStore) GetDependents(ctx context.Context, id string) ([]Dependency, error) {
	_ = ctx
	_ = id
	return nil, notImplemented("GetDependents")
}

func (s *sqliteStore) GetTags(ctx context.Context, id string) ([]string, error) {
	_ = ctx
	_ = id
	return nil, notImplemented("GetTags")
}

func (s *sqliteStore) GetPRs(ctx context.Context, id string) ([]PR, error) {
	_ = ctx
	_ = id
	return nil, notImplemented("GetPRs")
}

func (s *sqliteStore) GetNotes(ctx context.Context, id string) ([]Note, error) {
	_ = ctx
	_ = id
	return nil, notImplemented("GetNotes")
}

package store

import (
	"fmt"

	"github.com/mocky/quest/internal/errors"
)

// CheckOwnership enforces the owner-or-elevated policy used by
// `quest update`, `complete`, and `fail` when
// `enforce_session_ownership` is true (spec §Role Gating > Session
// ownership). Callers gate invocation on the config flag; when
// enforcement is off, this helper is not called and a non-owning,
// non-elevated caller proceeds. When enforcement is on, only the
// owning session (or an elevated role) can mutate the task. Empty
// ownerSession (the direct-close-by-lead case) fails for non-elevated
// callers because the semantics of "anyone can close a task nobody
// owns" would leak worker-level access into the elevated surface.
//
// The helper is deliberately pure — callers pass the pre-loaded
// owner_session string plus the decision booleans. No tx or store
// handle is required, so the function stays in this package only for
// discoverability (the policy belongs with the other task-level
// invariants).
func CheckOwnership(ownerSession, callerSession string, elevated bool) error {
	if elevated {
		return nil
	}
	if ownerSession != "" && ownerSession == callerSession {
		return nil
	}
	return fmt.Errorf("task is owned by another session: %w", errors.ErrPermission)
}

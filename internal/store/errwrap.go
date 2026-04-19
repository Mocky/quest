package store

import (
	stderrors "errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/mocky/quest/internal/errors"
)

// classifyDriverErr maps a driver error to the wrapped sentinel every
// handler switches on. Only SQLITE_BUSY (code 5, plus its extended
// variants) is retryable — that is the contract surfaced as exit code
// 7 in quest-spec.md §Storage. All other driver errors wrap
// ErrGeneral; upstream handlers (accept, complete, ...) translate
// precondition failures to ErrConflict before the error ever reaches
// this helper.
func classifyDriverErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if stderrors.As(err, &se) {
		// Extended SQLITE_BUSY codes (BUSY_RECOVERY, BUSY_SNAPSHOT,
		// BUSY_TIMEOUT) all share the primary code in the low byte.
		if primary := se.Code() & 0xFF; primary == sqlite3.SQLITE_BUSY {
			return fmt.Errorf("%w: %s", errors.ErrTransient, se.Error())
		}
	}
	return fmt.Errorf("%w: %s", errors.ErrGeneral, err.Error())
}

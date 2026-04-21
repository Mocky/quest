package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/mocky/quest/internal/errors"
)

// AppendHistory writes one row to the history table inside the
// transaction tx. Every state-changing mutation calls this exactly
// once per quest-spec.md §History field; idempotent no-ops
// (duplicate tag add, missing tag remove, duplicate PR URL) skip the
// call entirely.
//
// Empty Role and Session strings persist as SQL NULL per spec §History
// field ("Recorded as `null` if unset"). Doing the empty-string →
// sql.NullString{} translation here keeps the nullable-column
// contract enforced at a single call site; handlers never have to
// remember to pass nullables.
//
// Payload is marshaled to JSON; nil or empty payloads write the
// empty-object literal "{}" to match the schema default and the
// spec's action-specific-fields convention.
func AppendHistory(ctx context.Context, tx *Tx, h History) error {
	if tx == nil {
		return fmt.Errorf("%w: AppendHistory: nil transaction", errors.ErrGeneral)
	}
	if h.TaskID == "" {
		return fmt.Errorf("%w: AppendHistory: task_id is required", errors.ErrGeneral)
	}
	if h.Timestamp == "" {
		return fmt.Errorf("%w: AppendHistory: timestamp is required", errors.ErrGeneral)
	}
	if h.Action == "" {
		return fmt.Errorf("%w: AppendHistory: action is required", errors.ErrGeneral)
	}
	payload := "{}"
	if len(h.Payload) > 0 {
		b, err := json.Marshal(h.Payload)
		if err != nil {
			return fmt.Errorf("%w: AppendHistory: marshal payload: %s", errors.ErrGeneral, err.Error())
		}
		payload = string(b)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO history(task_id, timestamp, role, session, action, payload)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		h.TaskID,
		h.Timestamp,
		nullable(h.Role),
		nullable(h.Session),
		string(h.Action),
		payload,
	)
	if err != nil {
		return classifyDriverErr(err)
	}
	return nil
}

// nullable converts an empty Go string to sql.NullString{} so the
// INSERT persists SQL NULL, not "". Keeping the conversion rule at
// the write path ensures direct-SQL inspection sees NULL.
func nullable(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

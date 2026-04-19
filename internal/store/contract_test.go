//go:build integration

package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/store"
)

// freshStore opens a migrated SQLite DB at t.TempDir() and returns the
// store plus the on-disk path for tests that want to inspect the raw
// columns through a sibling *sql.DB.
func freshStore(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quest.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := store.Migrate(context.Background(), s); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return s, path
}

// seedTaskRow inserts a minimum-viable task row so AppendHistory's FK
// to tasks does not trip. Mirrors the helper in command_test.go but
// kept local so the store package's contract suite runs without the
// command package.
func seedTaskRow(t *testing.T, s store.Store, id string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, created_at) VALUES (?, ?, ?)`,
		id, id, "2026-04-19T00:00:00Z"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestHistoryEntryShape pins the spec §History field invariants:
//
//   - Every action enum value is non-empty so the wire contract has
//     no silent regressions.
//   - AppendHistory persists empty Role / Session as SQL NULL via the
//     single-call-site nullable() helper. cross-cutting.md §Nullable
//     TEXT columns guarantees direct-SQL inspection sees NULL, not "".
//   - Round-tripping through GetHistory preserves the action and
//     restores empty Role/Session as Go zero strings (the rendering
//     layer in command/show.go re-emits them as JSON null via *string).
//   - The created action's payload captures non-default planning
//     fields (tier/role/type/parent/tags/dependencies); read-back
//     restores them via Payload's map[string]any.
func TestHistoryEntryShape(t *testing.T) {
	t.Run("ActionEnumNonEmpty", func(t *testing.T) {
		actions := []store.HistoryAction{
			store.HistoryCreated,
			store.HistoryAccepted,
			store.HistoryCompleted,
			store.HistoryFailed,
			store.HistoryCancelled,
			store.HistoryReset,
			store.HistoryMoved,
			store.HistoryNoteAdded,
			store.HistoryPRAdded,
			store.HistoryFieldUpdated,
			store.HistoryLinked,
			store.HistoryUnlinked,
			store.HistoryTagged,
			store.HistoryUntagged,
			store.HistoryHandoffSet,
		}
		for _, a := range actions {
			if string(a) == "" {
				t.Errorf("history action constant is empty")
			}
		}
	})

	t.Run("EmptyRoleAndSessionPersistAsNULL", func(t *testing.T) {
		s, path := freshStore(t)
		seedTaskRow(t, s, "proj-a1")

		tx, err := s.BeginImmediate(context.Background(), store.TxAccept)
		if err != nil {
			t.Fatalf("BeginImmediate: %v", err)
		}
		if err := store.AppendHistory(context.Background(), tx, store.History{
			TaskID:    "proj-a1",
			Timestamp: "2026-04-19T00:00:01Z",
			Action:    store.HistoryAccepted,
			// Role and Session intentionally empty.
		}); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}

		// Open a sibling SQL connection and inspect the raw columns —
		// the cross-cutting nullable-TEXT contract only holds at the
		// write path, so a regression that introduces "" instead of
		// NULL is invisible to GetHistory but would surface here.
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		defer db.Close()
		var roleNull, sessNull bool
		if err := db.QueryRow(
			`SELECT role IS NULL, session IS NULL FROM history WHERE task_id = ? AND action = 'accepted'`,
			"proj-a1").Scan(&roleNull, &sessNull); err != nil {
			t.Fatalf("query history: %v", err)
		}
		if !roleNull {
			t.Errorf("role column = '', want NULL")
		}
		if !sessNull {
			t.Errorf("session column = '', want NULL")
		}
	})

	t.Run("RoundTripPreservesAction", func(t *testing.T) {
		s, _ := freshStore(t)
		seedTaskRow(t, s, "proj-a1")

		tx, err := s.BeginImmediate(context.Background(), store.TxUpdate)
		if err != nil {
			t.Fatalf("BeginImmediate: %v", err)
		}
		if err := store.AppendHistory(context.Background(), tx, store.History{
			TaskID:    "proj-a1",
			Timestamp: "2026-04-19T00:00:02Z",
			Role:      "planner",
			Session:   "sess-1",
			Action:    store.HistoryFieldUpdated,
			Payload:   map[string]any{"fields": map[string]any{"tier": map[string]any{"from": "T2", "to": "T3"}}},
		}); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}

		hist, err := s.GetHistory(context.Background(), "proj-a1")
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		if len(hist) != 1 {
			t.Fatalf("history = %d entries, want 1", len(hist))
		}
		got := hist[0]
		if got.Action != store.HistoryFieldUpdated {
			t.Errorf("action = %q, want %q", got.Action, store.HistoryFieldUpdated)
		}
		if got.Role != "planner" || got.Session != "sess-1" {
			t.Errorf("role/session = %q/%q, want planner/sess-1", got.Role, got.Session)
		}
		fields, ok := got.Payload["fields"].(map[string]any)
		if !ok {
			t.Fatalf("payload.fields missing or wrong type: %T", got.Payload["fields"])
		}
		tier, ok := fields["tier"].(map[string]any)
		if !ok {
			t.Fatalf("payload.fields.tier wrong type: %T", fields["tier"])
		}
		if tier["from"] != "T2" || tier["to"] != "T3" {
			t.Errorf("tier delta = %v, want from=T2 to=T3", tier)
		}
	})

	t.Run("CreatedPayloadCapturesNonDefaults", func(t *testing.T) {
		s, _ := freshStore(t)
		seedTaskRow(t, s, "proj-a1")

		tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
		if err != nil {
			t.Fatalf("BeginImmediate: %v", err)
		}
		if err := store.AppendHistory(context.Background(), tx, store.History{
			TaskID:    "proj-a1",
			Timestamp: "2026-04-19T00:00:03Z",
			Role:      "planner",
			Session:   "sess-1",
			Action:    store.HistoryCreated,
			Payload: map[string]any{
				"tier": "T2",
				"role": "coder",
				"type": "bug",
				"tags": []string{"auth", "go"},
				"dependencies": []map[string]any{
					{"target": "proj-a0", "link_type": "blocked-by"},
				},
			},
		}); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}

		hist, err := s.GetHistory(context.Background(), "proj-a1")
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		if len(hist) != 1 {
			t.Fatalf("history = %d, want 1", len(hist))
		}
		p := hist[0].Payload
		want := []string{"dependencies", "role", "tags", "tier", "type"}
		got := make([]string, 0, len(p))
		for k := range p {
			got = append(got, k)
		}
		sort.Strings(got)
		if len(got) != len(want) {
			t.Fatalf("payload keys = %v, want %v", got, want)
		}
		for i, k := range want {
			if got[i] != k {
				t.Errorf("payload key[%d] = %q, want %q", i, got[i], k)
			}
		}
	})

	t.Run("MissingRequiredArgsReturnsError", func(t *testing.T) {
		s, _ := freshStore(t)
		seedTaskRow(t, s, "proj-a1")
		tx, err := s.BeginImmediate(context.Background(), store.TxAccept)
		if err != nil {
			t.Fatalf("BeginImmediate: %v", err)
		}
		defer tx.Rollback()

		cases := []struct {
			name string
			h    store.History
		}{
			{"missing task_id", store.History{Timestamp: "2026-04-19T00:00:04Z", Action: store.HistoryAccepted}},
			{"missing timestamp", store.History{TaskID: "proj-a1", Action: store.HistoryAccepted}},
			{"missing action", store.History{TaskID: "proj-a1", Timestamp: "2026-04-19T00:00:04Z"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := store.AppendHistory(context.Background(), tx, tc.h); err == nil {
					t.Errorf("AppendHistory(%s): got nil error, want failure", tc.name)
				}
			})
		}
	})
}

// _ keeps json import live for future payload-shape assertions.
var _ = json.Marshal

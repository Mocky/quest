//go:build integration

package command_test

import (
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

// TestCreateStoresSeverity covers the happy path: `quest create
// --severity <valid>` writes the severity column and records it in the
// created history payload.
func TestCreateStoresSeverity(t *testing.T) {
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		t.Run(sev, func(t *testing.T) {
			s, dbPath := testStore(t)
			err, stdout, _ := runCreate(t, s, createCfg(),
				[]string{"--title", "Bug " + sev, "--severity", sev})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			id := ackID(t, stdout)

			var stored sql.NullString
			queryOne(t, dbPath,
				"SELECT severity FROM tasks WHERE id='"+id+"'").Scan(&stored)
			if !stored.Valid || stored.String != sev {
				t.Errorf("severity col = %+v, want %q", stored, sev)
			}

			var payload string
			queryOne(t, dbPath,
				"SELECT payload FROM history WHERE task_id='"+id+"' AND action='created'").Scan(&payload)
			var m map[string]any
			if jerr := json.Unmarshal([]byte(payload), &m); jerr != nil {
				t.Fatalf("unmarshal payload: %v", jerr)
			}
			if m["severity"] != sev {
				t.Errorf("payload.severity = %v, want %q", m["severity"], sev)
			}
		})
	}
}

// TestCreateUnsetSeverityPersistsAsNull: absence of --severity leaves
// the column NULL and omits it from the history payload.
func TestCreateUnsetSeverityPersistsAsNull(t *testing.T) {
	s, dbPath := testStore(t)
	err, stdout, _ := runCreate(t, s, createCfg(), []string{"--title", "No sev"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := ackID(t, stdout)

	var stored sql.NullString
	queryOne(t, dbPath, "SELECT severity FROM tasks WHERE id='"+id+"'").Scan(&stored)
	if stored.Valid {
		t.Errorf("severity = %q, want SQL NULL", stored.String)
	}

	var payload string
	queryOne(t, dbPath,
		"SELECT payload FROM history WHERE task_id='"+id+"' AND action='created'").Scan(&payload)
	if strings.Contains(payload, `"severity"`) {
		t.Errorf("unset severity should be omitted from payload; payload=%q", payload)
	}
}

// TestCreateInvalidSeverity: unknown or wrong-casing values return
// exit 2 via ErrUsage before any DB work happens. Spec §Planning
// fields severity enum is case-sensitive lowercase.
func TestCreateInvalidSeverity(t *testing.T) {
	for _, sev := range []string{"urgent", "Critical", "CRITICAL", "trivial"} {
		t.Run(sev, func(t *testing.T) {
			s, _ := testStore(t)
			err, _, _ := runCreate(t, s, createCfg(),
				[]string{"--title", "Bad", "--severity", sev})
			if err == nil || !stderrors.Is(err, errors.ErrUsage) {
				t.Fatalf("err = %v, want ErrUsage", err)
			}
		})
	}
}

// TestCreateEmptySeverityRejected: `--severity ""` mirrors the rule
// for --role / --tier — empty is a usage error.
func TestCreateEmptySeverityRejected(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runCreate(t, s, createCfg(),
		[]string{"--title", "Blank", "--severity", ""})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestUpdateSeverityElevatedAllowed: an elevated role can set severity
// on an open task; history records the field_updated row.
func TestUpdateSeverityElevatedAllowed(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--severity", "critical"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	var stored sql.NullString
	queryOne(t, dbPath, "SELECT severity FROM tasks WHERE id='proj-a1'").Scan(&stored)
	if !stored.Valid || stored.String != "critical" {
		t.Errorf("severity = %+v, want critical", stored)
	}

	var payload string
	queryOne(t, dbPath,
		"SELECT payload FROM history WHERE task_id='proj-a1' AND action='field_updated'").Scan(&payload)
	var p map[string]any
	if jerr := json.Unmarshal([]byte(payload), &p); jerr != nil {
		t.Fatalf("payload: %v", jerr)
	}
	fields, _ := p["fields"].(map[string]any)
	sev, _ := fields["severity"].(map[string]any)
	if sev["from"] != nil || sev["to"] != "critical" {
		t.Errorf("payload.fields.severity = %v, want {from: nil, to: critical}", sev)
	}
}

// TestUpdateSeverityByWorkerDenied: --severity is elevated-only, so a
// worker role hits the mixed-flag gate (exit 6).
func TestUpdateSeverityByWorkerDenied(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, workerCfg("sess-w1"), "", []string{"proj-a1", "--severity", "high"})
	if err == nil || !stderrors.Is(err, errors.ErrRoleDenied) {
		t.Fatalf("err = %v, want wraps ErrRoleDenied", err)
	}
}

// TestUpdateInvalidSeverityRejected: bad enum value exits 2 even as a
// planner, matching the tier-enum rule.
func TestUpdateInvalidSeverityRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "open", "")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--severity", "urgent"})
	if err == nil || !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestUpdateSeverityBlockedOnTerminalState: elevated-only severity
// edits are blocked on completed/failed/cancelled tasks. Completed
// takes precedence over the usage check.
func TestUpdateSeverityBlockedOnTerminalState(t *testing.T) {
	s, _ := testStore(t)
	seedTaskFull(t, s, "proj-a1", "Alpha", "completed", "")

	err, _, _ := runUpdate(t, s, plannerCfg(), "", []string{"proj-a1", "--severity", "high"})
	if err == nil || !stderrors.Is(err, errors.ErrConflict) {
		t.Fatalf("err = %v, want wraps ErrConflict", err)
	}
}

// TestListSeverityFilter: --severity narrows by value with OR-within-
// dimension (CSV + repeated flag), ANDs with other dimensions, and
// excludes NULL-severity tasks when active. Unknown values exit 2.
func TestListSeverityFilter(t *testing.T) {
	s, _ := testStore(t)
	seedListTaskWithSeverity(t, s, "proj-a1", "open", "critical")
	seedListTaskWithSeverity(t, s, "proj-a2", "open", "high")
	seedListTaskWithSeverity(t, s, "proj-a3", "open", "medium")
	seedListTaskWithSeverity(t, s, "proj-a4", "open", "")

	t.Run("single-value", func(t *testing.T) {
		err, stdout, _ := runList(t, s, plannerCfg(), []string{"--severity", "critical"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		rows := parseListArray(t, stdout)
		if len(rows) != 1 || string(rows[0]["id"]) != `"proj-a1"` {
			t.Errorf("rows = %+v, want [proj-a1]", rows)
		}
	})
	t.Run("csv-or", func(t *testing.T) {
		err, stdout, _ := runList(t, s, plannerCfg(), []string{"--severity", "critical,high"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		rows := parseListArray(t, stdout)
		if len(rows) != 2 {
			t.Errorf("rows = %d, want 2", len(rows))
		}
	})
	t.Run("repeat-accumulates", func(t *testing.T) {
		err, stdout, _ := runList(t, s, plannerCfg(),
			[]string{"--severity", "critical", "--severity", "medium"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		rows := parseListArray(t, stdout)
		if len(rows) != 2 {
			t.Errorf("rows = %d, want 2", len(rows))
		}
	})
	t.Run("null-excluded", func(t *testing.T) {
		err, stdout, _ := runList(t, s, plannerCfg(),
			[]string{"--severity", "critical,high,medium,low"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		rows := parseListArray(t, stdout)
		if len(rows) != 3 {
			t.Errorf("rows = %d, want 3 (null-severity row excluded)", len(rows))
		}
	})
	t.Run("unknown-rejected", func(t *testing.T) {
		err, _, _ := runList(t, s, plannerCfg(), []string{"--severity", "urgent"})
		if err == nil || !stderrors.Is(err, errors.ErrUsage) {
			t.Fatalf("err = %v, want wraps ErrUsage", err)
		}
	})
	t.Run("combined-with-status", func(t *testing.T) {
		err, stdout, _ := runList(t, s, plannerCfg(),
			[]string{"--severity", "critical", "--status", "open"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		rows := parseListArray(t, stdout)
		if len(rows) != 1 {
			t.Errorf("rows = %d, want 1 (AND across dimensions)", len(rows))
		}
	})
}

// TestListSeverityColumn: --columns severity is accepted, the row
// emits the enum value or null; default list omits severity.
func TestListSeverityColumn(t *testing.T) {
	s, _ := testStore(t)
	seedListTaskWithSeverity(t, s, "proj-a1", "open", "critical")
	seedListTaskWithSeverity(t, s, "proj-a2", "open", "")

	err, stdout, _ := runList(t, s, plannerCfg(),
		[]string{"--columns", "id,severity"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rows := parseListArray(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if string(rows[0]["severity"]) != `"critical"` {
		t.Errorf("row0.severity = %s, want \"critical\"", rows[0]["severity"])
	}
	if string(rows[1]["severity"]) != "null" {
		t.Errorf("row1.severity = %s, want null", rows[1]["severity"])
	}

	// Default column set does not include severity.
	err, stdout, _ = runList(t, s, plannerCfg(), nil)
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	if strings.Contains(stdout, `"severity"`) {
		t.Errorf("default list should not include severity; raw=%q", stdout)
	}
}

// TestShowJSONEmitsSeverity: quest show always emits the severity key;
// set → string value, unset → JSON null. Position follows tier in the
// response shape (ordering is covered by the contract test, we just
// pin presence here).
func TestShowJSONEmitsSeverity(t *testing.T) {
	s, _ := testStore(t)
	seedListTaskWithSeverity(t, s, "proj-a1", "open", "critical")
	seedListTaskWithSeverity(t, s, "proj-a2", "open", "")

	err, stdout, _ := runHandler(t, command.Show, s, baseCfg(), []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Show with severity: %v", err)
	}
	var resp map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if string(resp["severity"]) != `"critical"` {
		t.Errorf("severity = %s, want \"critical\"", resp["severity"])
	}

	err, stdout, _ = runHandler(t, command.Show, s, baseCfg(), []string{"proj-a2"}, "")
	if err != nil {
		t.Fatalf("Show without severity: %v", err)
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("unmarshal null: %v", jerr)
	}
	if string(resp["severity"]) != "null" {
		t.Errorf("severity = %s, want null", resp["severity"])
	}
}

// TestShowTextRendersSeverity: text-mode show renders a `severity` row
// in the metadata cluster when set, omits it when null.
func TestShowTextRendersSeverity(t *testing.T) {
	s, _ := testStore(t)
	seedListTaskWithSeverity(t, s, "proj-a1", "open", "high")
	seedListTaskWithSeverity(t, s, "proj-a2", "open", "")

	cfg := baseCfg()
	cfg.Output.Text = true

	err, stdout, _ := runHandler(t, command.Show, s, cfg, []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Show text: %v", err)
	}
	if !strings.Contains(stdout, "severity") || !strings.Contains(stdout, "high") {
		t.Errorf("text output missing severity row: %q", stdout)
	}

	err, stdout, _ = runHandler(t, command.Show, s, cfg, []string{"proj-a2"}, "")
	if err != nil {
		t.Fatalf("Show text null: %v", err)
	}
	if strings.Contains(stdout, "severity") {
		t.Errorf("null severity should not render: %q", stdout)
	}
}

// TestGraphJSONEmitsSeverity: quest graph nodes carry severity as a
// *string (value or null).
func TestGraphJSONEmitsSeverity(t *testing.T) {
	s, _ := testStore(t)
	seedListTaskWithSeverity(t, s, "proj-a1", "open", "critical")

	err, stdout, _ := runHandler(t, command.Graph, s, plannerCfg(), []string{"proj-a1"}, "")
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	var resp struct {
		Nodes []map[string]json.RawMessage `json:"nodes"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if len(resp.Nodes) == 0 {
		t.Fatalf("no nodes; raw=%q", stdout)
	}
	if string(resp.Nodes[0]["severity"]) != `"critical"` {
		t.Errorf("node severity = %s, want \"critical\"", resp.Nodes[0]["severity"])
	}
}

// TestBatchInvalidSeverityEmitsSemanticError: batch validation surfaces
// bad severity values with the new invalid_severity error code, matching
// the invalid_tier shape.
func TestBatchInvalidSeverityEmitsSemanticError(t *testing.T) {
	s, _ := testStore(t)
	path := writeBatchFile(t, `{"title":"Bad","severity":"urgent"}`+"\n")

	err, _, stderr := runBatch(t, s, createCfg(), []string{path})
	if err == nil {
		t.Fatal("batch: expected failure")
	}
	if !strings.Contains(stderr, `"code":"invalid_severity"`) {
		t.Errorf("stderr missing invalid_severity: %q", stderr)
	}
	if !strings.Contains(stderr, `"field":"severity"`) {
		t.Errorf("stderr missing field=severity: %q", stderr)
	}
	if !strings.Contains(stderr, `"value":"urgent"`) {
		t.Errorf("stderr missing value=urgent: %q", stderr)
	}
}

// TestBatchValidSeverityPersists: a batch line with a valid severity
// value lands in the DB and the created history payload.
func TestBatchValidSeverityPersists(t *testing.T) {
	s, dbPath := testStore(t)
	path := writeBatchFile(t, `{"ref":"root","title":"Bug","severity":"critical"}`+"\n")

	err, stdout, stderr := runBatch(t, s, createCfg(), []string{path})
	if err != nil {
		t.Fatalf("batch: %v (stderr=%q)", err, stderr)
	}
	// Pull the id from the first (only) ref/id pair on stdout.
	var pair map[string]string
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &pair); jerr != nil {
		t.Fatalf("stdout parse: %v; raw=%q", jerr, stdout)
	}
	id := pair["id"]
	if id == "" {
		t.Fatalf("stdout missing id: %q", stdout)
	}

	var stored sql.NullString
	queryOne(t, dbPath, "SELECT severity FROM tasks WHERE id='"+id+"'").Scan(&stored)
	if !stored.Valid || stored.String != "critical" {
		t.Errorf("severity = %+v, want critical", stored)
	}

	var payload string
	queryOne(t, dbPath,
		"SELECT payload FROM history WHERE task_id='"+id+"' AND action='created'").Scan(&payload)
	if !strings.Contains(payload, `"severity":"critical"`) {
		t.Errorf("payload missing severity: %q", payload)
	}
}

// seedListTaskWithSeverity is a thin wrapper that inserts a task with
// an explicit severity (empty → NULL). Severity is not part of the
// existing seedListTask signature, so this helper handles the column
// directly to keep per-test noise low.
func seedListTaskWithSeverity(t *testing.T, s store.Store, id, status, sev string) {
	t.Helper()
	tx, err := s.BeginImmediate(context.Background(), store.TxCreate)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	defer tx.Rollback()
	var sevArg any = sql.NullString{}
	if sev != "" {
		sevArg = sev
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO tasks(id, title, status, severity, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, id, status, sevArg, "2026-04-18T00:00:00Z"); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

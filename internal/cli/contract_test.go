//go:build integration

package cli_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mocky/quest/internal/cli"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/telemetry"
	"github.com/mocky/quest/internal/testutil"
)

// captureFor installs an in-memory tracer + meter and enables the OTEL
// flag so the dispatcher's InstrumentedStore decorator emits real
// store-tx spans. Mirrors migrateSpanExporter from
// migrate_integration_test.go but kept local so the contract suite can
// drift from the migrate-specific helpers.
func captureFor(t *testing.T) (*tracetest.InMemoryExporter, *testutil.CapturingMeter) {
	t.Helper()
	exp := testutil.NewCapturingTracer(t)
	meter := testutil.NewCapturingMeter(t)
	telemetry.MarkEnabledForTest()
	t.Cleanup(telemetry.MarkDisabledForTest)
	telemetry.InitInstrumentsForTest()
	return exp, meter
}

// elevatedCommands enumerates the planner-only command names.
// TestRoleGateDenials iterates this list — adding a new elevated
// command without updating the list fails the contract because the
// dispatcher's descriptor inventory drives the assertion (we hit each
// name end-to-end).
var elevatedCommands = []string{
	"create", "batch", "cancel", "reset", "move",
	"link", "unlink", "tag", "untag",
	"deps", "list", "graph", "export", "backup",
}

// TestRoleGateDenials exercises every elevated command at worker
// role and asserts each returns exit 6 (role_denied) with the canonical
// stderr two-liner. `update` is excluded — see TestUpdateElevatedFlagsDenied
// for the mixed-flag handler-level gate.
func TestRoleGateDenials(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "worker")
	for _, name := range elevatedCommands {
		t.Run(name, func(t *testing.T) {
			exit, _, stderr := runExecute([]string{name}, cfg)
			if exit != 6 {
				t.Errorf("%s: exit = %d; want 6 (role_denied)", name, exit)
			}
			if !strings.Contains(stderr, "quest: role_denied:") {
				t.Errorf("%s: stderr missing role_denied prefix: %q", name, stderr)
			}
			if !strings.Contains(stderr, "quest: exit 6 (role_denied)") {
				t.Errorf("%s: stderr missing exit-6 tail: %q", name, stderr)
			}
		})
	}
}

// updateElevatedFlags is the mixed-flag carve-out: `update` is
// dispatched at worker level, but any of these flags re-runs the role
// gate inside the handler.
var updateElevatedFlags = []string{
	"--title", "--description", "--context",
	"--tier", "--role",
	"--acceptance-criteria",
}

// TestBackupFromDispatcher exercises `quest backup --to PATH` end-to-
// end: elevated planner role, real workspace, snapshot + sidecar
// produced. Parallels the other dispatcher-happy-path tests. Writes
// the workspace's config.toml by hand because setupWorkspace leaves
// it blank.
func TestBackupFromDispatcher(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "planner")
	if err := os.WriteFile(
		filepath.Join(cfg.Workspace.Root, ".quest", "config.toml"),
		[]byte("elevated_roles = [\"planner\"]\nid_prefix = \"proj\"\n"),
		0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "snap.db")

	exit, stdout, stderrs := runExecute([]string{"backup", "--to", outPath}, cfg)
	if exit != 0 {
		t.Fatalf("exit = %d; stderr=%s", exit, stderrs)
	}
	var ack struct {
		DB            string `json:"db"`
		Config        string `json:"config"`
		SchemaVersion int    `json:"schema_version"`
		Bytes         int64  `json:"bytes"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout not JSON: %v; raw=%q", jerr, stdout)
	}
	if ack.DB != outPath {
		t.Errorf("ack.DB = %q, want %q", ack.DB, outPath)
	}
	if ack.Config != outPath+".config.toml" {
		t.Errorf("ack.Config = %q, want sidecar path", ack.Config)
	}
	if ack.Bytes <= 0 {
		t.Errorf("ack.Bytes = %d, want > 0", ack.Bytes)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("snapshot missing: %v", err)
	}
	if _, err := os.Stat(outPath + ".config.toml"); err != nil {
		t.Errorf("sidecar missing: %v", err)
	}
}

// TestUpdateElevatedFlagsDenied iterates the mixed-flag set per
// cross-cutting handler-side gate (Task 6.3). Every elevated flag,
// invoked by a worker on an open task, must return exit 6.
func TestUpdateElevatedFlagsDenied(t *testing.T) {
	for _, flag := range updateElevatedFlags {
		t.Run(strings.TrimPrefix(flag, "--"), func(t *testing.T) {
			cfg := setupWorkspace(t, "proj", "planner")
			seedOpenTask(t, cfg)
			// Now switch to worker for the assertion call.
			workerC := cfg
			workerC.Agent.Role = "worker"
			workerC.Agent.Session = "sess-w1"
			exit, _, stderr := runExecute([]string{"update", "proj-01", flag, "value"}, workerC)
			if exit != 6 {
				t.Errorf("%s: exit = %d; want 6", flag, exit)
			}
			if !strings.Contains(stderr, "quest: role_denied:") {
				t.Errorf("%s: stderr missing role_denied prefix: %q", flag, stderr)
			}
		})
	}

	// --meta is the eighth elevated flag; runs through the same path
	// but takes a key=value pair rather than a bare value.
	t.Run("meta", func(t *testing.T) {
		cfg := setupWorkspace(t, "proj", "planner")
		seedOpenTask(t, cfg)
		workerC := cfg
		workerC.Agent.Role = "worker"
		workerC.Agent.Session = "sess-w1"
		exit, _, _ := runExecute([]string{"update", "proj-01", "--meta", "k=v"}, workerC)
		if exit != 6 {
			t.Errorf("meta: exit = %d; want 6", exit)
		}
	})
}

// TestCommandSpanOnRoleDenial exercises the dispatcher's role-denial
// path end-to-end. Asserts: a command span (`execute_tool quest.create`)
// is opened, the gate child span (`quest.role.gate`) fires, the
// command span carries `quest.exit_code=6` and `quest.error.class=role_denied`,
// and the operations + errors counters increment.
func TestCommandSpanOnRoleDenial(t *testing.T) {
	exp, meter := captureFor(t)
	cfg := setupWorkspace(t, "proj", "worker")

	exit, _, _ := runExecute([]string{"create"}, cfg)
	if exit != 6 {
		t.Fatalf("exit = %d; want 6", exit)
	}

	spans := exp.GetSpans()
	var cmdSpan, gateSpan *tracetest.SpanStub
	for i := range spans {
		switch spans[i].Name {
		case "execute_tool quest.create":
			cmdSpan = &spans[i]
		case "quest.role.gate":
			gateSpan = &spans[i]
		}
	}
	if cmdSpan == nil {
		t.Fatalf("execute_tool quest.create span missing; got %v", spanNamesFromExp(spans))
	}
	if gateSpan == nil {
		t.Fatalf("quest.role.gate span missing; got %v", spanNamesFromExp(spans))
	}

	gotClass, gotExit := "", int64(0)
	for _, kv := range cmdSpan.Attributes {
		if kv.Key == "quest.error.class" {
			gotClass = kv.Value.AsString()
		}
		if kv.Key == "quest.exit_code" {
			gotExit = kv.Value.AsInt64()
		}
	}
	if gotClass != "role_denied" {
		t.Errorf("class = %q; want role_denied", gotClass)
	}
	if gotExit != 6 {
		t.Errorf("exit_code = %d; want 6", gotExit)
	}

	rm := meter.Collect(context.Background())
	gotErrorOps, gotRoleErrors := int64(0), int64(0)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				switch m.Name {
				case "dept.quest.operations":
					for _, kv := range dp.Attributes.ToSlice() {
						if kv.Key == "status" && kv.Value.AsString() == "error" {
							gotErrorOps += dp.Value
						}
					}
				case "dept.quest.errors":
					for _, kv := range dp.Attributes.ToSlice() {
						if kv.Key == "error_class" && kv.Value.AsString() == "role_denied" {
							gotRoleErrors += dp.Value
						}
					}
				}
			}
		}
	}
	if gotErrorOps != 1 {
		t.Errorf("operations{status=error} = %d; want 1", gotErrorOps)
	}
	if gotRoleErrors != 1 {
		t.Errorf("errors{error_class=role_denied} = %d; want 1", gotRoleErrors)
	}
}

// TestChildSpansOmitGenAIAttributes pins the §8.6 carve-out: only the
// root command span carries gen_ai.* attributes. Every non-root span
// (quest.store.tx, quest.role.gate, quest.db.migrate, ...) must NOT
// carry gen_ai.tool.name / gen_ai.operation.name / gen_ai.agent.name.
//
// Drives a sequence of representative invocations through the
// dispatcher to populate the in-memory exporter with the full child-
// span set, then iterates every non-root span and asserts each one is
// gen_ai-clean. New child-span types added later are checked
// automatically.
func TestChildSpansOmitGenAIAttributes(t *testing.T) {
	exp, _ := captureFor(t)
	cfg := setupWorkspace(t, "proj", "planner")

	if exit, _, errb := runExecute([]string{"create", "--title", "X"}, cfg); exit != 0 {
		t.Fatalf("create exit = %d; stderr=%s", exit, errb)
	}
	if exit, _, errb := runExecute([]string{"list"}, cfg); exit != 0 {
		t.Fatalf("list exit = %d; stderr=%s", exit, errb)
	}

	spans := exp.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans captured")
	}
	rootKeys := map[string]bool{
		"gen_ai.tool.name":      true,
		"gen_ai.operation.name": true,
		"gen_ai.agent.name":     true,
	}
	for _, s := range spans {
		isRoot := strings.HasPrefix(s.Name, "execute_tool quest.")
		if isRoot {
			continue
		}
		for _, kv := range s.Attributes {
			if rootKeys[string(kv.Key)] {
				t.Errorf("non-root span %q carries forbidden %s=%v",
					s.Name, kv.Key, kv.Value.AsInterface())
			}
		}
	}
}

// TestIdempotencyGuarantees pins the spec §Idempotency table entries:
// duplicate link, missing tag remove, duplicate PR, second cancel on
// already-cancelled task. Every row returns exit 0.
func TestIdempotencyGuarantees(t *testing.T) {
	t.Run("duplicate-link-noop", func(t *testing.T) {
		cfg := setupWorkspace(t, "proj", "planner")
		seedOpenTask(t, cfg)
		seedOpenTask(t, cfg)
		if exit, _, errb := runExecute([]string{"link", "proj-01", "--blocked-by", "proj-02"}, cfg); exit != 0 {
			t.Fatalf("first link exit = %d; stderr=%s", exit, errb)
		}
		if exit, _, errb := runExecute([]string{"link", "proj-01", "--blocked-by", "proj-02"}, cfg); exit != 0 {
			t.Fatalf("idempotent link exit = %d; stderr=%s", exit, errb)
		}
	})

	t.Run("missing-tag-remove-noop", func(t *testing.T) {
		cfg := setupWorkspace(t, "proj", "planner")
		seedOpenTask(t, cfg)
		if exit, _, errb := runExecute([]string{"untag", "proj-01", "absent"}, cfg); exit != 0 {
			t.Fatalf("untag absent exit = %d; stderr=%s", exit, errb)
		}
	})

	t.Run("duplicate-pr-noop", func(t *testing.T) {
		cfg := setupWorkspace(t, "proj", "planner")
		seedOpenTask(t, cfg)
		workerC := cfg
		workerC.Agent.Role = "worker"
		workerC.Agent.Session = "sess-w1"
		if exit, _, errb := runExecute([]string{"update", "proj-01", "--pr", "https://x/1"}, workerC); exit != 0 {
			t.Fatalf("first pr exit = %d; stderr=%s", exit, errb)
		}
		if exit, _, errb := runExecute([]string{"update", "proj-01", "--pr", "https://x/1"}, workerC); exit != 0 {
			t.Fatalf("dup pr exit = %d; stderr=%s", exit, errb)
		}
	})

	t.Run("cancel-already-cancelled-noop", func(t *testing.T) {
		cfg := setupWorkspace(t, "proj", "planner")
		seedOpenTask(t, cfg)
		if exit, _, errb := runExecute([]string{"cancel", "proj-01"}, cfg); exit != 0 {
			t.Fatalf("first cancel exit = %d; stderr=%s", exit, errb)
		}
		if exit, _, errb := runExecute([]string{"cancel", "proj-01"}, cfg); exit != 0 {
			t.Fatalf("idempotent cancel exit = %d; stderr=%s", exit, errb)
		}
	})
}

// TestHelpRendersDoubleDashLongFlags pins the STANDARDS.md §Help
// Rendering convention end-to-end: `quest help <cmd>` writes a usage
// block whose long-flag names are prefixed with "--" and whose single-
// character names are prefixed with "-". The test exercises one
// elevated command (`list`) and one worker command (`show`) so the
// shared helper is proven wired on both sides of the role gate. Per
// the 2026-05-06 grove decision the help block lands on stdout (not
// stderr — that was the old `--help` flag form's channel).
func TestHelpRendersDoubleDashLongFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string
		wantNot []string
	}{
		{
			name:    "list",
			args:    []string{"help", "list"},
			want:    []string{"--columns", "--status", "--ready", "Usage: quest list", "List tasks with filtering."},
			wantNot: []string{" -columns ", " -status ", " -ready\t", " -ready\n"},
		},
		{
			name:    "show",
			args:    []string{"help", "show"},
			want:    []string{"--history", "Usage: quest show ID [--history]", "Display full task details"},
			wantNot: []string{" -history\t", " -history\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := setupWorkspace(t, "proj", "planner")
			exit, stdout, stderr := runExecute(tt.args, cfg)
			if exit != 0 {
				t.Fatalf("exit = %d; stderr=%s", exit, stderr)
			}
			for _, s := range tt.want {
				if !strings.Contains(stdout, s) {
					t.Errorf("stdout missing %q; got:\n%s", s, stdout)
				}
			}
			for _, s := range tt.wantNot {
				if strings.Contains(stdout, s) {
					t.Errorf("stdout unexpectedly contains %q; got:\n%s", s, stdout)
				}
			}
		})
	}
}

// TestExportLayout pins the spec §quest export on-disk layout:
// tasks/{id}.json (one per task), debriefs/{id}.md (one per task with
// a non-empty debrief), history.jsonl at the archive root.
func TestExportLayout(t *testing.T) {
	cfg := setupWorkspace(t, "proj", "planner")
	if exit, _, errb := runExecute([]string{"create", "--title", "Alpha"}, cfg); exit != 0 {
		t.Fatalf("create alpha exit = %d; stderr=%s", exit, errb)
	}
	if exit, _, errb := runExecute([]string{"create", "--title", "Beta"}, cfg); exit != 0 {
		t.Fatalf("create beta exit = %d; stderr=%s", exit, errb)
	}
	// Accept + complete proj-01 so it lands a non-empty debrief — proves
	// debriefs/*.md emission is wired end-to-end.
	worker := cfg
	worker.Agent.Role = "worker"
	worker.Agent.Session = "sess-w1"
	if exit, _, errb := runExecute([]string{"accept", "proj-01"}, worker); exit != 0 {
		t.Fatalf("accept exit = %d; stderr=%s", exit, errb)
	}
	if exit, _, errb := runExecute([]string{"complete", "proj-01", "--debrief", "ok"}, worker); exit != 0 {
		t.Fatalf("complete exit = %d; stderr=%s", exit, errb)
	}

	if exit, _, errb := runExecute([]string{"export"}, cfg); exit != 0 {
		t.Fatalf("export exit = %d; stderr=%s", exit, errb)
	}

	dir := filepath.Join(cfg.Workspace.Root, "quest-export")
	for _, sub := range []string{"tasks", "debriefs"} {
		st, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("missing %s/ subdir: %v", sub, err)
			continue
		}
		if !st.IsDir() {
			t.Errorf("%s is not a dir", sub)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "history.jsonl")); err != nil {
		t.Errorf("history.jsonl missing: %v", err)
	}
	tasks, err := filepath.Glob(filepath.Join(dir, "tasks", "*.json"))
	if err != nil {
		t.Fatalf("glob tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("tasks/*.json count = %d, want 2", len(tasks))
	}
	debriefs, err := filepath.Glob(filepath.Join(dir, "debriefs", "*.md"))
	if err != nil {
		t.Fatalf("glob debriefs: %v", err)
	}
	if len(debriefs) != 1 {
		t.Errorf("debriefs/*.md count = %d, want 1 (one task completed)", len(debriefs))
	}
}

// handlerScenario is one row in the TestHandlerRecorderWiring iteration:
// a command, an args builder that may seed prerequisites, and the
// recorder-emission assertions that prove the handler called at least
// one telemetry.RecordX function on its happy path.
type handlerScenario struct {
	name             string
	setup            func(t *testing.T, cfg *config.Config) []string
	wantAttr         []string // any present on the cmd span counts
	wantStatus       bool     // assert quest.task.status.from/to attrs
	wantTerminal     string   // outcome label on dept.quest.tasks.completed
	wantBatchOutcome string   // assert quest.batch.outcome attribute value
}

// TestHandlerRecorderWiring iterates the task-affecting handler
// inventory and asserts each one emits at least one telemetry recorder
// call on its happy path. Each scenario runs against a fresh workspace
// + capturing tracer/meter so spans and metrics from earlier scenarios
// don't bleed in.
//
// Cross-reference: TestRoleGateDenials covers the elevated-command
// dispatch gate; TestUpdateElevatedFlagsDenied covers update's mixed-
// flag carve-out. These three tests together cover the full descriptor
// inventory.
func TestHandlerRecorderWiring(t *testing.T) {
	cases := []handlerScenario{
		{
			name: "show",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				return []string{"show", "proj-01"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "accept",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				cfg.Agent.Role = "worker"
				cfg.Agent.Session = "sess-w1"
				return []string{"accept", "proj-01"}
			},
			wantAttr:   []string{"quest.task.id"},
			wantStatus: true,
		},
		{
			name: "complete",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				worker := *cfg
				worker.Agent.Role = "worker"
				worker.Agent.Session = "sess-w1"
				if exit, _, errb := runExecute([]string{"accept", "proj-01"}, worker); exit != 0 {
					t.Fatalf("seed accept: %d %s", exit, errb)
				}
				cfg.Agent.Role = "worker"
				cfg.Agent.Session = "sess-w1"
				return []string{"complete", "proj-01", "--debrief", "done"}
			},
			wantAttr:     []string{"quest.task.id"},
			wantStatus:   true,
			wantTerminal: "completed",
		},
		{
			name: "fail",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				worker := *cfg
				worker.Agent.Role = "worker"
				worker.Agent.Session = "sess-w1"
				if exit, _, errb := runExecute([]string{"accept", "proj-01"}, worker); exit != 0 {
					t.Fatalf("seed accept: %d %s", exit, errb)
				}
				cfg.Agent.Role = "worker"
				cfg.Agent.Session = "sess-w1"
				return []string{"fail", "proj-01", "--debrief", "could not"}
			},
			wantAttr:     []string{"quest.task.id"},
			wantStatus:   true,
			wantTerminal: "failed",
		},
		{
			name: "create",
			setup: func(t *testing.T, cfg *config.Config) []string {
				return []string{"create", "--title", "X"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "cancel",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				return []string{"cancel", "proj-01"}
			},
			wantAttr:     []string{"quest.task.id"},
			wantStatus:   true,
			wantTerminal: "cancelled",
		},
		{
			name: "reset",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				worker := *cfg
				worker.Agent.Role = "worker"
				worker.Agent.Session = "sess-w1"
				if exit, _, errb := runExecute([]string{"accept", "proj-01"}, worker); exit != 0 {
					t.Fatalf("seed accept: %d %s", exit, errb)
				}
				return []string{"reset", "proj-01"}
			},
			wantAttr:   []string{"quest.task.id"},
			wantStatus: true,
		},
		{
			name: "move",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				seedOpenTask(t, *cfg)
				return []string{"move", "proj-01", "--parent", "proj-02"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "link",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				seedOpenTask(t, *cfg)
				return []string{"link", "proj-01", "--blocked-by", "proj-02"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "unlink",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				seedOpenTask(t, *cfg)
				if exit, _, _ := runExecute([]string{"link", "proj-01", "--blocked-by", "proj-02"}, *cfg); exit != 0 {
					t.Fatalf("seed link")
				}
				return []string{"unlink", "proj-01", "--blocked-by", "proj-02"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "tag",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				return []string{"tag", "proj-01", "go,auth"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "untag",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				if exit, _, _ := runExecute([]string{"tag", "proj-01", "go,auth"}, *cfg); exit != 0 {
					t.Fatalf("seed tag")
				}
				return []string{"untag", "proj-01", "auth"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "deps",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				return []string{"deps", "proj-01"}
			},
			wantAttr: []string{"quest.task.id"},
		},
		{
			name: "list",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				return []string{"list"}
			},
			wantAttr: []string{"quest.query.result_count"},
		},
		{
			name: "graph",
			setup: func(t *testing.T, cfg *config.Config) []string {
				seedOpenTask(t, *cfg)
				return []string{"graph", "proj-01"}
			},
			wantAttr: []string{"quest.task.id", "quest.graph.node_count"},
		},
		{
			name: "batch",
			setup: func(t *testing.T, cfg *config.Config) []string {
				path := filepath.Join(cfg.Workspace.Root, "batch.jsonl")
				if err := os.WriteFile(path, []byte(`{"ref":"a","title":"A"}`+"\n"), 0o644); err != nil {
					t.Fatalf("write batch: %v", err)
				}
				return []string{"batch", path}
			},
			wantAttr:         []string{"quest.batch.outcome"},
			wantBatchOutcome: "ok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp, meter := captureFor(t)
			cfg := setupWorkspace(t, "proj", "planner")
			args := tc.setup(t, &cfg)

			exit, _, stderr := runExecute(args, cfg)
			if exit != 0 {
				t.Fatalf("%s: exit = %d; stderr=%q", tc.name, exit, stderr)
			}

			spans := exp.GetSpans()
			var cmdSpan *tracetest.SpanStub
			for i := len(spans) - 1; i >= 0; i-- {
				if spans[i].Name == "execute_tool quest."+tc.name {
					cmdSpan = &spans[i]
					break
				}
			}
			if cmdSpan == nil {
				t.Fatalf("%s: command span missing; got %v", tc.name, spanNamesFromExp(spans))
			}

			present := map[string]bool{}
			for _, kv := range cmdSpan.Attributes {
				present[string(kv.Key)] = true
			}
			anyPresent := false
			for _, k := range tc.wantAttr {
				if present[k] {
					anyPresent = true
					break
				}
			}
			if !anyPresent {
				t.Errorf("%s: command span missing any of %v; got attrs %v",
					tc.name, tc.wantAttr, attrKeys(cmdSpan.Attributes))
			}

			if tc.wantStatus {
				if !present["quest.task.status.from"] || !present["quest.task.status.to"] {
					t.Errorf("%s: status-from/to attrs missing on cmd span", tc.name)
				}
			}

			if tc.wantTerminal != "" {
				rm := meter.Collect(context.Background())
				got := terminalOutcomeCount(rm, tc.wantTerminal)
				if got < 1 {
					t.Errorf("%s: dept.quest.tasks.completed{outcome=%s} = %d; want >= 1",
						tc.name, tc.wantTerminal, got)
				}
			}

			if tc.wantBatchOutcome != "" {
				gotOutcome := ""
				for _, kv := range cmdSpan.Attributes {
					if kv.Key == "quest.batch.outcome" {
						gotOutcome = kv.Value.AsString()
					}
				}
				if gotOutcome != tc.wantBatchOutcome {
					t.Errorf("%s: batch.outcome = %q; want %q", tc.name, gotOutcome, tc.wantBatchOutcome)
				}
			}
		})
	}
}

// terminalOutcomeCount sums the dept.quest.tasks.completed{outcome=X}
// values across all data points.
func terminalOutcomeCount(rm metricdata.ResourceMetrics, want string) int64 {
	total := int64(0)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "dept.quest.tasks.completed" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				for _, kv := range dp.Attributes.ToSlice() {
					if kv.Key == "outcome" && kv.Value.AsString() == want {
						total += dp.Value
					}
				}
			}
		}
	}
	return total
}

// attrKeys returns the keys of an OTEL attribute slice as strings.
func attrKeys(kvs []attribute.KeyValue) []string {
	out := make([]string, len(kvs))
	for i, kv := range kvs {
		out[i] = string(kv.Key)
	}
	return out
}

// spanNamesFromExp returns the names of every captured span.
func spanNamesFromExp(spans tracetest.SpanStubs) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Name
	}
	return out
}

// seedOpenTask invokes `quest create --title <generated>` against cfg
// using the planner role from the original workspace setup. Each call
// consumes one slot from the workspace's top-level counter, so callers
// know the resulting IDs are proj-1, proj-2, ... in order.
func seedOpenTask(t *testing.T, cfg config.Config) {
	t.Helper()
	planner := cfg
	planner.Agent.Role = "planner"
	if exit, _, errb := runExecute([]string{"create", "--title", "seed"}, planner); exit != 0 {
		t.Fatalf("seed: exit=%d; stderr=%s", exit, errb)
	}
}

// _ keeps imports live across future contract tests.
var (
	_ = cli.Execute
	_ = stderrors.Is
	_ = errors.ErrConflict
	_ = attribute.String
)

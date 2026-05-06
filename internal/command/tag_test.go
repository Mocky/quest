//go:build integration

package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/store"
)

func runTag(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Tag(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

func runUntag(t *testing.T, s store.Store, cfg config.Config, args []string) (error, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := command.Untag(context.Background(), cfg, s, args, strings.NewReader(""), &out, &errb)
	return err, out.String(), errb.String()
}

// TestTagHappyPath: a single tag round-trips through the post-state ack
// and lands a single history row.
func TestTagHappyPath(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	err, stdout, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go"})
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	var ack struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ack); jerr != nil {
		t.Fatalf("stdout: %v; raw=%q", jerr, stdout)
	}
	if ack.ID != "proj-a1" {
		t.Errorf("id = %q, want proj-a1", ack.ID)
	}
	if len(ack.Tags) != 1 || ack.Tags[0] != "go" {
		t.Errorf("tags = %v, want [go]", ack.Tags)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM tags WHERE task_id='proj-a1' AND tag='go'").Scan(&n)
	if n != 1 {
		t.Errorf("tag rows = %d, want 1", n)
	}
	var h int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='tagged'").Scan(&h)
	if h != 1 {
		t.Errorf("history rows = %d, want 1", h)
	}
}

// TestTagMultipleSorted: multi-tag input returns sorted post-state.
func TestTagMultipleSorted(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	err, stdout, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "zeta,alpha,go"})
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	var ack struct {
		Tags []string `json:"tags"`
	}
	_ = json.Unmarshal([]byte(stdout), &ack)
	want := []string{"alpha", "go", "zeta"}
	if len(ack.Tags) != 3 || ack.Tags[0] != want[0] || ack.Tags[1] != want[1] || ack.Tags[2] != want[2] {
		t.Errorf("tags = %v, want %v", ack.Tags, want)
	}
}

// TestTagLowercasesInput: uppercase + mixed-case input becomes lowercase.
func TestTagLowercasesInput(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	if err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "GO,Auth"}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	var golower, authlower int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM tags WHERE task_id='proj-a1' AND tag='go'").Scan(&golower)
	queryOne(t, dbPath, "SELECT COUNT(*) FROM tags WHERE task_id='proj-a1' AND tag='auth'").Scan(&authlower)
	if golower != 1 || authlower != 1 {
		t.Errorf("rows go/auth = %d/%d, want 1/1", golower, authlower)
	}
}

// TestTagInvalidPatternRejected: a tag with whitespace fails exit 2.
func TestTagInvalidPatternRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go,bad tag"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestTagInvalidStartChar: tag must start with alphanumeric.
func TestTagInvalidStartChar(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "-leading-dash"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestTagOversizedRejected: a tag > 32 chars fails exit 2.
func TestTagOversizedRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	long := strings.Repeat("a", 33)
	err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", long})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestTagDuplicateNoOp: the same tag twice in one call does not double-
// add and does not write history twice.
func TestTagDuplicateInputDeduped(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	if err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go,go"}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM tags WHERE task_id='proj-a1' AND tag='go'").Scan(&n)
	if n != 1 {
		t.Errorf("dedup rows = %d, want 1", n)
	}
}

// TestTagAlreadyPresentNoOp: repeated invocation does not re-write
// history.
func TestTagAlreadyPresentNoOp(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	if err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	err, stdout, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !strings.Contains(stdout, `"tags":["go"]`) {
		t.Errorf("stdout = %q", stdout)
	}
	var h int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='tagged'").Scan(&h)
	if h != 1 {
		t.Errorf("history rows = %d, want 1 (no second history)", h)
	}
}

// TestTagPartialNoOpHistoryReflectsChanged: when half the input is
// already present and half is new, history records only the new ones.
func TestTagPartialNoOpHistoryReflectsChanged(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	if err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go,auth"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	var payload string
	queryOne(t, dbPath,
		"SELECT payload FROM history WHERE task_id='proj-a1' AND action='tagged' ORDER BY id DESC LIMIT 1").
		Scan(&payload)
	if !strings.Contains(payload, `"auth"`) || strings.Contains(payload, `"go"`) {
		t.Errorf("history payload = %q, want only changed (auth, not go)", payload)
	}
}

// TestTagUnknownTask: missing task → exit 3.
func TestTagUnknownTask(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-x", "go"})
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestTagMissingTags: ID without TAGS → exit 2.
func TestTagMissingTags(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}

// TestUntagRoundTrip: tag then untag → no rows.
func TestUntagRoundTrip(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	if err, _, _ := runTag(t, s, plannerCfg(), []string{"proj-a1", "go,auth"}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	err, stdout, _ := runUntag(t, s, plannerCfg(), []string{"proj-a1", "go"})
	if err != nil {
		t.Fatalf("Untag: %v", err)
	}
	if !strings.Contains(stdout, `"tags":["auth"]`) {
		t.Errorf("stdout = %q, want post-state [auth]", stdout)
	}
	var n int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM tags WHERE task_id='proj-a1' AND tag='go'").Scan(&n)
	if n != 0 {
		t.Errorf("go rows after untag = %d, want 0", n)
	}
	var u int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='untagged'").Scan(&u)
	if u != 1 {
		t.Errorf("untagged history = %d, want 1", u)
	}
}

// TestUntagAbsentNoOp: removing a tag that isn't there is a no-op.
func TestUntagAbsentNoOp(t *testing.T) {
	s, dbPath := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")

	err, stdout, _ := runUntag(t, s, plannerCfg(), []string{"proj-a1", "go"})
	if err != nil {
		t.Fatalf("Untag: %v", err)
	}
	if !strings.Contains(stdout, `"tags":[]`) {
		t.Errorf("stdout = %q, want empty post-state", stdout)
	}
	var u int
	queryOne(t, dbPath, "SELECT COUNT(*) FROM history WHERE task_id='proj-a1' AND action='untagged'").Scan(&u)
	if u != 0 {
		t.Errorf("untagged history = %d, want 0 (no-op)", u)
	}
}

// TestUntagUnknownTask: missing task → exit 3.
func TestUntagUnknownTask(t *testing.T) {
	s, _ := testStore(t)
	err, _, _ := runUntag(t, s, plannerCfg(), []string{"proj-x", "go"})
	if !stderrors.Is(err, errors.ErrNotFound) {
		t.Fatalf("err = %v, want wraps ErrNotFound", err)
	}
}

// TestUntagInvalidPatternRejected: invalid tag in untag list still
// fails exit 2 (validation runs the same way).
func TestUntagInvalidPatternRejected(t *testing.T) {
	s, _ := testStore(t)
	seedTaskWithStatus(t, s, "proj-a1", "A", "", "open")
	err, _, _ := runUntag(t, s, plannerCfg(), []string{"proj-a1", "BAD TAG"})
	if !stderrors.Is(err, errors.ErrUsage) {
		t.Fatalf("err = %v, want wraps ErrUsage", err)
	}
}


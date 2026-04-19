package logging_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/logging"
)

func TestLevelFromString(t *testing.T) {
	cases := []struct {
		in    string
		want  slog.Level
		wantK bool
	}{
		{"debug", slog.LevelDebug, true},
		{"DEBUG", slog.LevelDebug, true},
		{"info", slog.LevelInfo, true},
		{"INFO", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"ERROR", slog.LevelError, true},
		{"", slog.LevelInfo, false},
		{"garbage", slog.LevelInfo, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := logging.LevelFromString(tc.in)
			if got != tc.want || ok != tc.wantK {
				t.Errorf("LevelFromString(%q) = (%v, %v); want (%v, %v)", tc.in, got, ok, tc.want, tc.wantK)
			}
		})
	}
}

type recorder struct {
	mu      sync.Mutex
	level   slog.Level
	records []slog.Record
}

func (r *recorder) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= r.level
}
func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recorder) WithGroup(string) slog.Handler      { return r }

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

func TestSetupFanOutDispatchesToExtras(t *testing.T) {
	a := &recorder{level: slog.LevelDebug}
	b := &recorder{level: slog.LevelDebug}
	logger := logging.Setup(config.LogConfig{Level: "debug"}, a, b)

	logger.InfoContext(context.Background(), "hello")

	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("extras did not both receive the record: a=%d b=%d", a.count(), b.count())
	}
}

func TestSetupFanOutEnabledIfAnyChild(t *testing.T) {
	a := &recorder{level: slog.LevelError} // disabled at info
	b := &recorder{level: slog.LevelInfo}  // enabled at info
	logger := logging.Setup(config.LogConfig{Level: "info"}, a, b)

	logger.InfoContext(context.Background(), "hi")

	if a.count() != 0 {
		t.Errorf("higher-level child got a record: %d", a.count())
	}
	if b.count() != 1 {
		t.Errorf("lower-level child missed the record: %d", b.count())
	}
}

func TestSetupFanOutLevelGatedPerChild(t *testing.T) {
	child := &recorder{level: slog.LevelInfo}
	logger := logging.Setup(config.LogConfig{Level: "debug"}, child)

	logger.DebugContext(context.Background(), "verbose")
	if child.count() != 0 {
		t.Errorf("child at info level saw a debug record: %d", child.count())
	}

	logger.InfoContext(context.Background(), "report")
	if child.count() != 1 {
		t.Errorf("child at info level missed an info record: %d", child.count())
	}
}

func TestSetupSkipsNilExtras(t *testing.T) {
	logger := logging.Setup(config.LogConfig{Level: "info"}, nil)
	if logger == nil {
		t.Fatal("Setup returned nil logger")
	}
	// Non-panic is the assertion; no observable stderr verification here.
	logger.InfoContext(context.Background(), "ok")
}

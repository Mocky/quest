package config

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

type slogRecorder struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (r *slogRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *slogRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *slogRecorder) WithGroup(string) slog.Handler            { return r }
func (r *slogRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	return nil
}

func (r *slogRecorder) records() []slog.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]slog.Record, len(r.recs))
	copy(out, r.recs)
	return out
}

func (r *slogRecorder) has(msg string) bool {
	for _, rec := range r.records() {
		if rec.Message == msg {
			return true
		}
	}
	return false
}

func (r *slogRecorder) count(msg string) int {
	n := 0
	for _, rec := range r.records() {
		if rec.Message == msg {
			n++
		}
	}
	return n
}

// captureSlog swaps slog.Default with a recorder for the test's lifetime.
func captureSlog(t *testing.T) *slogRecorder {
	t.Helper()
	prev := slog.Default()
	rec := &slogRecorder{}
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

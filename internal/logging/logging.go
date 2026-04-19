package logging

import (
	"log/slog"
	"os"
	"strings"

	"github.com/mocky/quest/internal/config"
)

func Setup(cfg config.LogConfig, extras ...slog.Handler) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}
	var h slog.Handler = slog.NewJSONHandler(os.Stderr, opts)
	_ = extras
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

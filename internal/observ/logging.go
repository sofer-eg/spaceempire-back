// Package observ holds the observability wiring (phase 7.1): structured
// logging, Prometheus metrics, and the basic-auth gate for /metrics and
// /debug/*.
package observ

import (
	"io"
	"log/slog"
	"os"
	"strings"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"

	"spaceempire/back/internal/pkg/config"
)

// NewLogger builds the application logger from config. With LogFile set it
// writes rotated JSON (lumberjack) — the production path; otherwise it writes
// the chosen format to stdout. Falls back to a sensible default on a bad
// level/format string rather than failing startup.
func NewLogger(cfg config.ObservabilityConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}

	var (
		w      io.Writer = os.Stdout
		asJSON           = strings.EqualFold(cfg.LogFormat, "json")
	)
	if cfg.LogFile != "" {
		w = &lumberjack.Logger{
			Filename:   cfg.LogFile,
			MaxSize:    cfg.LogMaxSizeMB,
			MaxBackups: cfg.LogMaxBackups,
			MaxAge:     cfg.LogMaxAgeDays,
			Compress:   true,
		}
		asJSON = true // a rotated log file is always JSON (machine-parsed)
	}

	var h slog.Handler
	if asJSON {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
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

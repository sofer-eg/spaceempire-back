// Package observ holds the observability wiring (phase 7.1): structured
// logging, Prometheus metrics, and the basic-auth gate for /metrics and
// /debug/*.
package observ

import (
	"context"
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
	return slog.New(WithRequestID(h))
}

// WithRequestID wraps h so every record logged with a request context carries
// the `request_id` AccessLog assigned (phase 8.11). slog's own JSON/Text
// handlers ignore context values, so without this only AccessLog's access line
// has an id: a handler's `logger.ErrorContext(ctx, …)` could not be correlated
// with the request that caused it, which is the whole point of the field.
// Wrapping the handler rather than each call site means no handler has to
// thread the id by hand.
func WithRequestID(h slog.Handler) slog.Handler { return requestIDHandler{h} }

type requestIDHandler struct{ slog.Handler }

func (h requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	// AccessLog attaches the field itself — it has to, because it may wrap a
	// logger this handler never saw (AccessLog(nil) falls back to
	// slog.Default()). Skipping a record that already carries the key keeps
	// that one line from emitting request_id twice in the same JSON object.
	if id, ok := RequestIDFromContext(ctx); ok && !hasAttr(r, requestIDAttr) {
		r.AddAttrs(slog.String(requestIDAttr, id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup must re-wrap the handler slog hands back, or every
// logger derived with logger.With(…) silently loses the injection — and the
// sector worker, sector pool, quest pacer and story picker all derive theirs
// that way. Note that under WithGroup the attribute nests inside the open
// group like any other record attr; nothing in the app opens one today.
func (h requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestIDHandler{h.Handler.WithAttrs(attrs)}
}

func (h requestIDHandler) WithGroup(name string) slog.Handler {
	return requestIDHandler{h.Handler.WithGroup(name)}
}

// hasAttr reports whether r already carries an attribute named key.
func hasAttr(r slog.Record, key string) bool {
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found = true
			return false
		}
		return true
	})
	return found
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

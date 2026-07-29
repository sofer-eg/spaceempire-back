package observ_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/observ"
	"spaceempire/back/internal/pkg/config"
)

// hijackableRW is a ResponseWriter that supports hijacking (like the real Go
// HTTP/1.1 writer the WS upgrade needs).
type hijackableRW struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

// TestUnit_Middleware_PassesThroughHijack guards the WS regression: the
// status-recording wrapper in AccessLog + HTTPMiddleware must keep http.Hijacker
// reachable, or coder/websocket's Accept fails with "ResponseWriter does not
// implement http.Hijacker" and the whole real-time game breaks.
func TestUnit_Middleware_PassesThroughHijack(t *testing.T) {
	t.Parallel()
	base := &hijackableRW{ResponseRecorder: httptest.NewRecorder()}
	var sawHijacker bool
	var hijackErr error
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		sawHijacker = ok
		if ok {
			_, _, hijackErr = hj.Hijack()
		}
	})
	m := observ.NewMetrics()
	wrapped := observ.AccessLog(nil)(m.HTTPMiddleware(handler))
	wrapped.ServeHTTP(base, httptest.NewRequest(http.MethodGet, "/ws", nil))

	require.True(t, sawHijacker, "WS upgrade needs http.Hijacker through the middleware chain")
	require.NoError(t, hijackErr)
	require.True(t, base.hijacked, "Hijack must delegate to the underlying writer")
}

func TestUnit_Metrics_HandlerExposesCollectors(t *testing.T) {
	t.Parallel()
	m := observ.NewMetrics()
	m.RecordTick(1, 12*time.Millisecond, 3*time.Millisecond, 7, 2, 1.0)
	m.IncTickOverrun(0)
	m.SetQueueDepth(0, 4)
	m.IncHandoff(1, 2)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	text := string(body)

	for _, want := range []string{
		"se_tick_duration_ms",
		"se_ship_count",
		"se_tick_overrun_total",
		"se_command_queue_depth",
		"se_handoff_total",
		"se_time_scale",
		"se_dirty_count",
	} {
		assert.Contains(t, text, want, "metric %s should be exposed", want)
	}
}

func TestUnit_BasicAuth_OpenWhenUserEmpty(t *testing.T) {
	t.Parallel()
	h := observ.BasicAuth("", "", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusTeapot, rec.Code, "no gate when user is empty")
}

func TestUnit_BasicAuth_GatesWhenConfigured(t *testing.T) {
	t.Parallel()
	h := observ.BasicAuth("admin", "secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No credentials → 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Wrong credentials → 401.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.SetBasicAuth("admin", "wrong")
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Correct credentials → pass.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.SetBasicAuth("admin", "secret")
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// captureRequestCtx runs one request through AccessLog and returns the context
// the wrapped handler saw plus the id echoed in the response header. The
// context key is package-private, so this is the only honest way to obtain a
// context that really carries an id — the same way a real handler gets one.
func captureRequestCtx(t *testing.T, logger *slog.Logger) (context.Context, string) {
	t.Helper()
	var got context.Context
	h := observ.AccessLog(logger)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Context()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/thing", nil))
	id := rec.Header().Get("X-Request-ID")
	require.NotEmpty(t, id, "AccessLog must echo the id it assigned")
	require.NotNil(t, got, "the wrapped handler must have run")
	return got, id
}

// debugLogger builds a JSON logger over buf with the request-id injection in
// place. Debug level because AccessLog's own access line is logged at Debug.
func debugLogger(buf io.Writer) *slog.Logger {
	return slog.New(observ.WithRequestID(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
}

// TestUnit_WithRequestID_InjectsIntoHandlerLogs is the point of the wrapper:
// a handler that logs with the request context gets `request_id` on its own
// line, correlating it with the access line. slog's JSON/Text handlers ignore
// context values entirely, so without this nothing but AccessLog carries one.
func TestUnit_WithRequestID_InjectsIntoHandlerLogs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := debugLogger(&buf)
	ctx, id := captureRequestCtx(t, logger)

	logger.ErrorContext(ctx, "handler blew up")

	line, _ := recordWithMsg(t, buf.String(), "handler blew up")
	assert.Equal(t, id, line["request_id"],
		"the handler's own line must carry the id from the response header")
}

// TestUnit_WithRequestID_SurvivesLoggerWith guards the classic wrapper bug:
// slog returns a NEW handler from WithAttrs/WithGroup, so a wrapper that does
// not re-wrap silently loses its behaviour for every derived logger. The
// sector worker, sector pool, quest pacer and story picker all derive theirs
// with logger.With("component", …), so an unwrapped WithAttrs would strip the
// id from exactly the components most worth correlating.
func TestUnit_WithRequestID_SurvivesLoggerWith(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := debugLogger(&buf)
	ctx, id := captureRequestCtx(t, logger)

	derived := logger.With("component", "sector.worker")
	derived.ErrorContext(ctx, "derived line")

	line, _ := recordWithMsg(t, buf.String(), "derived line")
	assert.Equal(t, id, line["request_id"], "logger.With must not drop the injection")
	assert.Equal(t, "sector.worker", line["component"], "the derived attrs still apply")
}

// TestUnit_WithRequestID_SurvivesLoggerWithGroup is the WithGroup half of the
// same re-wrap contract — without it, deleting the WithGroup override leaves
// the whole suite green while every grouped logger silently loses the id.
//
// It also pins the SHAPE, which is the wart worth knowing about: a record attr
// added at Handle time lands INSIDE the open group, so the field is
// `db.request_id`, not top-level `request_id`. Nothing in the app opens a
// group today; the day something does (a repository logger is the obvious
// candidate), a log query on top-level request_id would quietly miss exactly
// those lines — and this test is where that shows up.
func TestUnit_WithRequestID_SurvivesLoggerWithGroup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := debugLogger(&buf)
	ctx, id := captureRequestCtx(t, logger)

	logger.WithGroup("db").ErrorContext(ctx, "query failed", "table", "ships")

	line, _ := recordWithMsg(t, buf.String(), "query failed")
	group, ok := line["db"].(map[string]any)
	require.True(t, ok, "the group is still emitted as a nested object")
	assert.Equal(t, id, group["request_id"], "logger.WithGroup must not drop the injection")
	assert.Equal(t, "ships", group["table"], "the grouped attrs still apply")
	assert.NotContains(t, line, "request_id", "documented wart: the id nests inside the open group")
}

// TestUnit_WithRequestID_AccessLineNotDuplicated: AccessLog attaches the field
// itself (it must, since it may wrap a logger this handler never saw — the
// AccessLog(nil) fallback goes to slog.Default()). The wrapper therefore has
// to skip a record that already carries the key, or the access line emits
// request_id twice in one JSON object.
func TestUnit_WithRequestID_AccessLineNotDuplicated(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	_, id := captureRequestCtx(t, debugLogger(&buf))

	line, raw := recordWithMsg(t, buf.String(), "http request")
	assert.Equal(t, id, line["request_id"])
	assert.Equal(t, 1, strings.Count(raw, `"request_id"`),
		"the access line must carry request_id exactly once")
}

// TestUnit_WithRequestID_AbsentWithoutRequest: background logging (workers,
// startup) has no request context and must not grow an empty or bogus field.
func TestUnit_WithRequestID_AbsentWithoutRequest(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	debugLogger(&buf).Info("tick done")

	line, _ := recordWithMsg(t, buf.String(), "tick done")
	_, present := line["request_id"]
	assert.False(t, present, "no request context → no request_id attribute")
}

// TestUnit_NewLogger_WiresRequestID closes the gap the tests above leave: they
// all build their own wrapped handler, so dropping WithRequestID from
// NewLogger would leave every one of them green while the running server lost
// the field entirely. Exercising the production path (LogFile set → rotated
// JSON) is the only way to assert the wiring itself.
func TestUnit_NewLogger_WiresRequestID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "app.log")
	logger := observ.NewLogger(config.ObservabilityConfig{
		LogLevel: "debug",
		LogFile:  path,
	})
	ctx, id := captureRequestCtx(t, logger)
	logger.ErrorContext(ctx, "wired through NewLogger")

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	_, raw := recordWithMsg(t, string(out), "wired through NewLogger")
	assert.Contains(t, raw, `"request_id":"`+id+`"`,
		"the logger NewLogger actually returns must inject the request id")
}

// recordWithMsg returns the first log record in out whose "msg" is exactly
// want, both parsed and raw. Matching the PARSED msg rather than a substring
// of the raw line matters: a line that merely mentions want inside an
// attribute value would otherwise be picked up instead, and the assert built
// on it would quietly stop checking anything.
func recordWithMsg(t *testing.T, out, want string) (map[string]any, string) {
	t.Helper()
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(raw), &rec) != nil {
			continue
		}
		if rec["msg"] == want {
			return rec, raw
		}
	}
	t.Fatalf("no log line with msg %q in:\n%s", want, out)
	return nil, ""
}

// compile-time check that Metrics satisfies the sector sink shape.
var _ interface {
	RecordTick(domain.SectorID, time.Duration, time.Duration, int, int, float64)
	IncTickOverrun(int)
	SetQueueDepth(int, int)
	IncHandoff(domain.SectorID, domain.SectorID)
} = (*observ.Metrics)(nil)

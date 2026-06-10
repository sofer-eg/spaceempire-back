package observ_test

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/observ"
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

// compile-time check that Metrics satisfies the sector sink shape.
var _ interface {
	RecordTick(domain.SectorID, time.Duration, time.Duration, int, int, float64)
	IncTickOverrun(int)
	SetQueueDepth(int, int)
	IncHandoff(domain.SectorID, domain.SectorID)
} = (*observ.Metrics)(nil)

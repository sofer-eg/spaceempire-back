package observ

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"spaceempire/back/internal/domain"
)

// Metrics holds the Prometheus collectors and exposes the /metrics handler.
// It implements sector.MetricsSink (RecordTick / IncTickOverrun /
// SetQueueDepth / IncHandoff). One instance per process, wired in app.go and
// passed to the sector pool via sector.WithMetrics.
type Metrics struct {
	reg *prometheus.Registry

	tickDuration     *prometheus.HistogramVec
	snapshotDuration *prometheus.HistogramVec
	shipCount        *prometheus.GaugeVec
	dirtyCount       *prometheus.GaugeVec
	timeScale        *prometheus.GaugeVec
	tickOverrun      *prometheus.CounterVec
	queueDepth       *prometheus.GaugeVec
	handoffTotal     *prometheus.CounterVec
	httpDuration     *prometheus.HistogramVec
}

// NewMetrics builds the registry and registers every collector plus the Go
// runtime/process collectors. Uses a dedicated registry (not the global
// default) so repeated construction in tests never panics on double-register.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		tickDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "se_tick_duration_ms",
			Help:    "Sector tick duration in milliseconds.",
			Buckets: msBuckets,
		}, []string{"sector"}),
		snapshotDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "se_snapshot_duration_ms",
			Help:    "Sector snapshot+broadcast duration in milliseconds.",
			Buckets: msBuckets,
		}, []string{"sector"}),
		shipCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "se_ship_count",
			Help: "Live ships per sector.",
		}, []string{"sector"}),
		dirtyCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "se_dirty_count",
			Help: "Ships pending persistence (dirty) per sector.",
		}, []string{"sector"}),
		timeScale: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "se_time_scale",
			Help: "Sector time-dilation factor (1.0 = real time).",
		}, []string{"sector"}),
		tickOverrun: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "se_tick_overrun_total",
			Help: "Ticks that exceeded the tick interval, per worker.",
		}, []string{"worker"}),
		queueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "se_command_queue_depth",
			Help: "Inbox command-queue depth, per worker.",
		}, []string{"worker"}),
		handoffTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "se_handoff_total",
			Help: "Player sector handoffs, by from/to sector.",
		}, []string{"from", "to"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "se_http_request_duration_ms",
			Help:    "HTTP request duration in milliseconds, by method and status.",
			Buckets: msBuckets,
		}, []string{"method", "code"}),
	}
	reg.MustRegister(
		m.tickDuration, m.snapshotDuration, m.shipCount, m.dirtyCount, m.timeScale,
		m.tickOverrun, m.queueDepth, m.handoffTotal, m.httpDuration,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

var msBuckets = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// Handler returns the /metrics HTTP handler over this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// RegisterPoolStats wires pgx pool gauges (read live from pool.Stat on each
// scrape). Safe to call once after the pool is built.
func (m *Metrics) RegisterPoolStats(pool *pgxpool.Pool) {
	m.reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "se_db_pool_total_conns", Help: "Total pgx pool connections.",
		}, func() float64 { return float64(pool.Stat().TotalConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "se_db_pool_acquired_conns", Help: "Acquired (in-use) pgx pool connections.",
		}, func() float64 { return float64(pool.Stat().AcquiredConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "se_db_pool_idle_conns", Help: "Idle pgx pool connections.",
		}, func() float64 { return float64(pool.Stat().IdleConns()) }),
	)
}

// --- sector.MetricsSink ---

// RecordTick records one sector tick's duration, snapshot time, ship count and
// time-scale.
func (m *Metrics) RecordTick(sectorID domain.SectorID, tickDur, snapshotDur time.Duration, shipCount, dirtyCount int, timeScale float64) {
	label := strconv.FormatInt(int64(sectorID), 10)
	m.tickDuration.WithLabelValues(label).Observe(float64(tickDur.Microseconds()) / 1000)
	m.snapshotDuration.WithLabelValues(label).Observe(float64(snapshotDur.Microseconds()) / 1000)
	m.shipCount.WithLabelValues(label).Set(float64(shipCount))
	m.dirtyCount.WithLabelValues(label).Set(float64(dirtyCount))
	m.timeScale.WithLabelValues(label).Set(timeScale)
}

// IncTickOverrun counts a tick that exceeded its interval.
func (m *Metrics) IncTickOverrun(workerIdx int) {
	m.tickOverrun.WithLabelValues(strconv.Itoa(workerIdx)).Inc()
}

// SetQueueDepth reports a worker's inbox depth.
func (m *Metrics) SetQueueDepth(workerIdx, depth int) {
	m.queueDepth.WithLabelValues(strconv.Itoa(workerIdx)).Set(float64(depth))
}

// IncHandoff counts a player sector handoff.
func (m *Metrics) IncHandoff(from, to domain.SectorID) {
	m.handoffTotal.WithLabelValues(
		strconv.FormatInt(int64(from), 10),
		strconv.FormatInt(int64(to), 10),
	).Inc()
}

// --- HTTP ---

// HTTPMiddleware records request duration + status code for every request.
func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.httpDuration.WithLabelValues(r.Method, strconv.Itoa(rec.status)).
			Observe(float64(time.Since(start).Microseconds()) / 1000)
	})
}

// statusRecorder captures the response status code for the metric.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack passes through to the underlying connection so WebSocket upgrades
// (coder/websocket needs http.Hijacker) keep working when this status-recording
// wrapper sits in front of the /ws route. Embedding the http.ResponseWriter
// interface does not promote http.Hijacker, so without this the upgrade fails
// with "ResponseWriter does not implement http.Hijacker".
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("observ: ResponseWriter does not support hijacking")
	}
	return h.Hijack()
}

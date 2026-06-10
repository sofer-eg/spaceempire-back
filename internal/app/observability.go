package app

import (
	"encoding/json"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/observ"
	"spaceempire/back/internal/pkg/config"
	"spaceempire/back/internal/sector"
)

// registerObservability mounts /metrics and /debug/* on mux, all behind the
// basic-auth gate (open in dev when Observability.DebugUser is empty). Phase
// 7.1. pprof's subtree handler serves the named profiles (heap, goroutine,
// allocs, …); the four non-profile endpoints get explicit, more-specific
// routes.
func registerObservability(mux *http.ServeMux, m *observ.Metrics, sectorPool *sector.Pool, cfg *config.Config) {
	obs := cfg.Observability
	gate := func(h http.Handler) http.Handler { return observ.BasicAuth(obs.DebugUser, obs.DebugPass, h) }

	mux.Handle("GET /metrics", gate(m.Handler()))

	mux.Handle("/debug/pprof/", gate(http.HandlerFunc(pprof.Index)))
	mux.Handle("/debug/pprof/cmdline", gate(http.HandlerFunc(pprof.Cmdline)))
	mux.Handle("/debug/pprof/profile", gate(http.HandlerFunc(pprof.Profile)))
	mux.Handle("/debug/pprof/symbol", gate(http.HandlerFunc(pprof.Symbol)))
	mux.Handle("/debug/pprof/trace", gate(http.HandlerFunc(pprof.Trace)))

	// /debug/world?sector=X — JSON dump of a sector's live state.
	mux.Handle("GET /debug/world", gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid, err := strconv.ParseInt(r.URL.Query().Get("sector"), 10, 64)
		if err != nil || sid <= 0 {
			http.Error(w, `{"error":"sector query param required"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sectorPool.Snapshot(domain.SectorID(sid)))
	})))

	// /debug/config — the currently loaded balance.yaml, served verbatim.
	mux.Handle("GET /debug/config", gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(cfg.Balance.Path)
		if err != nil {
			http.Error(w, `{"error":"read balance file"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(data)
	})))
}

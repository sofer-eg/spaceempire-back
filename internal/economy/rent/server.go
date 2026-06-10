package rent

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
)

// Middleware is the auth gate applied to the rent route.
type Middleware = func(http.Handler) http.Handler

// Server exposes the rent HTTP read endpoint over a Service.
type Server struct {
	svc    *Service
	logger *slog.Logger
}

// NewServer constructs a Server. A nil logger falls back to slog.Default.
func NewServer(svc *Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{svc: svc, logger: logger}
}

// RegisterRoutes mounts the rent endpoints behind the auth middleware.
//
//	GET  /api/my/rents
//	POST /api/stations/{id}/claim
func (s *Server) RegisterRoutes(mux *http.ServeMux, authMW Middleware) {
	mux.Handle("GET /api/my/rents", authMW(http.HandlerFunc(s.handleMyRents)))
	mux.Handle("POST /api/stations/{id}/claim", authMW(http.HandlerFunc(s.handleClaim)))
}

// handleClaim lets the authenticated player claim an unowned station (8.7).
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"error":"invalid station id"}`, http.StatusBadRequest)
		return
	}
	switch err := s.svc.Claim(r.Context(), player, domain.StationID(id)); {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case errors.Is(err, ErrStationOwned), errors.Is(err, ErrInsufficientFunds):
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusConflict)
	default:
		s.logger.Error("rent: claim", "err", err, "player", int64(player), "station", id)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
	}
}

// rentDTO is the JSON shape of one rent in GET /api/my/rents.
type rentDTO struct {
	ID              int64      `json:"id"`
	StationKind     int        `json:"stationKind"`
	StationID       int64      `json:"stationId"`
	AmountPerPeriod int64      `json:"amountPerPeriod"`
	UnpaidPeriods   int        `json:"unpaidPeriods"`
	LastPaidAt      *time.Time `json:"lastPaidAt"`
	NextDueAt       time.Time  `json:"nextDueAt"`
}

func (s *Server) handleMyRents(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	rents, err := s.svc.MyRents(r.Context(), player)
	if err != nil {
		s.logger.Error("rent: my rents", "err", err, "player", int64(player))
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	out := make([]rentDTO, 0, len(rents))
	for _, rt := range rents {
		dto := rentDTO{
			ID:              int64(rt.ID),
			StationKind:     int(rt.Station.Kind),
			StationID:       rt.Station.ID,
			AmountPerPeriod: rt.AmountPerPeriod,
			UnpaidPeriods:   rt.UnpaidPeriods,
			NextDueAt:       rt.NextDueAt,
		}
		if !rt.LastPaidAt.IsZero() {
			t := rt.LastPaidAt
			dto.LastPaidAt = &t
		}
		out = append(out, dto)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

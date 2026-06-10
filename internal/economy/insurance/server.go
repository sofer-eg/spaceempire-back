package insurance

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
)

// Middleware is the auth gate applied to every insurance route.
type Middleware = func(http.Handler) http.Handler

// Server exposes the insurance HTTP handlers over a Service.
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

// RegisterRoutes mounts the insurance endpoints behind the auth middleware.
//
//	GET  /api/insurance
//	POST /api/insurance
func (s *Server) RegisterRoutes(mux *http.ServeMux, authMW Middleware) {
	mux.Handle("GET /api/insurance", authMW(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /api/insurance", authMW(http.HandlerFunc(s.handleBuy)))
}

type buyRequest struct {
	ShipID       int64 `json:"shipId"`
	Premium      int64 `json:"premium"`
	DurationDays int   `json:"durationDays"`
}

// policyDTO is the JSON shape of one policy in GET /api/insurance. EffStatus
// folds in time-lapse: an 'active' row past expires_at reads as "expired"
// (lazy expiry is only flushed to the DB on re-insure).
type policyDTO struct {
	ID          int64      `json:"id"`
	ShipID      int64      `json:"shipId"`
	PremiumPaid int64      `json:"premiumPaid"`
	Coverage    int64      `json:"coverage"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	ClaimedAt   *time.Time `json:"claimedAt"`
}

func (s *Server) handleBuy(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req buyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	id, err := s.svc.Buy(r.Context(), player, domain.ShipID(req.ShipID), req.Premium, req.DurationDays)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": int64(id)})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	policies, err := s.svc.MyPolicies(r.Context(), player)
	if err != nil {
		s.logger.Error("insurance: list", "err", err, "player", int64(player))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	now := time.Now()
	out := make([]policyDTO, 0, len(policies))
	for _, p := range policies {
		dto := policyDTO{
			ID:          int64(p.ID),
			ShipID:      int64(p.ShipID),
			PremiumPaid: p.PremiumPaid,
			Coverage:    p.Coverage,
			Status:      string(p.Status),
			CreatedAt:   p.CreatedAt,
			ExpiresAt:   p.ExpiresAt,
		}
		if p.Status == domain.PolicyActive && !p.ExpiresAt.After(now) {
			dto.Status = string(domain.PolicyExpired)
		}
		if !p.ClaimedAt.IsZero() {
			t := p.ClaimedAt
			dto.ClaimedAt = &t
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotOwner):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrShipNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNotDocked), errors.Is(err, ErrAlreadyInsured), errors.Is(err, ErrInsufficientFunds):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.logger.Error("insurance handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

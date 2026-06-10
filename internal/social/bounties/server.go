package bounties

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	bountyrepo "spaceempire/back/internal/persistence/bounties"
)

// Middleware is the auth gate applied to every bounty route.
type Middleware = func(http.Handler) http.Handler

// Server exposes the bounty HTTP handlers over a Service.
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

// RegisterRoutes mounts the bounty endpoints, each behind the auth middleware.
//
//	GET  /api/bounties
//	POST /api/bounties
//	GET  /api/players/{id}/bounty-history
func (s *Server) RegisterRoutes(mux *http.ServeMux, authMW Middleware) {
	h := func(fn http.HandlerFunc) http.Handler { return authMW(http.HandlerFunc(fn)) }
	mux.Handle("GET /api/bounties", h(s.handleList))
	mux.Handle("POST /api/bounties", h(s.handleSet))
	mux.Handle("GET /api/players/{id}/bounty-history", h(s.handleHistory))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	views, err := s.svc.TopActive(r.Context())
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTOs(views))
}

func (s *Server) handleSet(w http.ResponseWriter, r *http.Request) {
	caller, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req setRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	kind, ok := parseKind(req.TargetKind)
	if !ok || req.TargetID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid target")
		return
	}
	target := domain.EntityRef{Kind: kind, ID: req.TargetID}
	ttl := time.Duration(req.TTLHours) * time.Hour
	id, err := s.svc.SetBounty(r.Context(), caller, target, req.Amount, ttl, req.FromClan)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": int64(id)})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid player id")
		return
	}
	views, err := s.svc.History(r.Context(), domain.PlayerID(id))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTOs(views))
}

func toDTOs(views []bountyrepo.View) []bountyDTO {
	out := make([]bountyDTO, 0, len(views))
	for _, v := range views {
		out = append(out, toBountyDTO(v))
	}
	return out
}

// writeServiceError maps a Service error to an HTTP status.
func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrSelfBounty):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrInsufficientFunds), errors.Is(err, ErrNotInClan):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotClanLeader):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		s.logger.Error("bounties handler", "err", err)
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

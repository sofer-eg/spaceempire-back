package clans

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
)

// Middleware is the auth gate applied to every clan route (all clan
// operations require an authenticated player). app/ passes
// authServer.RequireAuth.
type Middleware = func(http.Handler) http.Handler

// Server exposes the clan HTTP handlers over a Service.
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

// RegisterRoutes mounts the clan endpoints on mux, each wrapped by the auth
// middleware. The literal /mine and /invites paths take precedence over the
// /{id} wildcard (Go 1.22 ServeMux specificity).
//
//	GET  /api/clans
//	POST /api/clans
//	GET  /api/clans/mine
//	GET  /api/clans/invites
//	GET  /api/clans/{id}
//	POST /api/clans/{id}/invite
//	POST /api/clans/{id}/accept
//	POST /api/clans/{id}/leave
//	POST /api/clans/{id}/kick
func (s *Server) RegisterRoutes(mux *http.ServeMux, authMW Middleware) {
	h := func(fn http.HandlerFunc) http.Handler { return authMW(http.HandlerFunc(fn)) }
	mux.Handle("GET /api/clans", h(s.handleList))
	mux.Handle("POST /api/clans", h(s.handleCreate))
	mux.Handle("GET /api/clans/mine", h(s.handleMine))
	mux.Handle("GET /api/clans/invites", h(s.handleMyInvites))
	mux.Handle("GET /api/clans/{id}", h(s.handleDetail))
	mux.Handle("POST /api/clans/{id}/invite", h(s.handleInvite))
	mux.Handle("POST /api/clans/{id}/accept", h(s.handleAccept))
	mux.Handle("POST /api/clans/{id}/leave", h(s.handleLeave))
	mux.Handle("POST /api/clans/{id}/kick", h(s.handleKick))
	mux.Handle("POST /api/clans/{id}/role", h(s.handleSetRole))
}

func (s *Server) handleSetRole(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	clanID, ok := parseClanID(w, r)
	if !ok {
		return
	}
	var req roleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.svc.SetRole(r.Context(), actor, clanID, domain.PlayerID(req.PlayerID), req.Role); err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	clans, err := s.svc.List(r.Context())
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	out := make([]clanSummaryDTO, 0, len(clans))
	for _, c := range clans {
		out = append(out, toClanSummaryDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	clan, err := s.svc.Create(r.Context(), player, req.Name, req.Tag)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, clanSummaryDTO{
		ID:          int64(clan.ID),
		Name:        clan.Name,
		Tag:         clan.Tag,
		LeaderID:    int64(clan.LeaderID),
		MemberCount: 1,
		CreatedAt:   clan.CreatedAt,
	})
}

func (s *Server) handleMine(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	detail, err := s.svc.MyClan(r.Context(), player)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if detail == nil {
		writeJSON(w, http.StatusOK, nil) // null — player is in no clan
		return
	}
	writeJSON(w, http.StatusOK, toClanDetailDTO(*detail))
}

func (s *Server) handleMyInvites(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	invites, err := s.svc.MyInvitations(r.Context(), player)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	out := make([]invitationDTO, 0, len(invites))
	for _, i := range invites {
		out = append(out, toInvitationDTO(i))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	clanID, ok := parseClanID(w, r)
	if !ok {
		return
	}
	detail, err := s.svc.Detail(r.Context(), clanID)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClanDetailDTO(detail))
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	clanID, ok := parseClanID(w, r)
	if !ok {
		return
	}
	var req playerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.svc.Invite(r.Context(), player, clanID, domain.PlayerID(req.PlayerID)); err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	clanID, ok := parseClanID(w, r)
	if !ok {
		return
	}
	if err := s.svc.Accept(r.Context(), player, clanID); err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	clanID, ok := parseClanID(w, r)
	if !ok {
		return
	}
	if err := s.svc.Leave(r.Context(), player, clanID); err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleKick(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	clanID, ok := parseClanID(w, r)
	if !ok {
		return
	}
	var req playerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.svc.Kick(r.Context(), actor, clanID, domain.PlayerID(req.PlayerID)); err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// parseClanID reads the {id} path value. On a bad id it writes 400 and
// returns ok=false.
func parseClanID(w http.ResponseWriter, r *http.Request) (domain.ClanID, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid clan id")
		return 0, false
	}
	return domain.ClanID(id), true
}

// writeServiceError maps a Service error to an HTTP status. Unknown errors
// are logged and surface as 500.
func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrInvalidRole):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNameTaken),
		errors.Is(err, ErrTagTaken),
		errors.Is(err, ErrAlreadyInClan),
		errors.Is(err, ErrAlreadyInvited),
		errors.Is(err, ErrCannotKickLeader),
		errors.Is(err, ErrCannotChangeLeader),
		errors.Is(err, ErrLeaderMustTransfer):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrForbidden),
		errors.Is(err, ErrNotMember):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrClanNotFound),
		errors.Is(err, ErrInvitationNotFound),
		errors.Is(err, ErrTargetNotMember):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		s.logger.Error("clans handler", "err", err)
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

package quest

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"spaceempire/back/internal/auth"
)

// Middleware is the auth gate applied to the quest routes.
type Middleware = func(http.Handler) http.Handler

// Server exposes the quest HTTP endpoints over a Service.
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

// RegisterRoutes mounts the quest endpoints behind the auth middleware.
func (s *Server) RegisterRoutes(mux *http.ServeMux, authMW Middleware) {
	mux.Handle("GET /api/quests/active", authMW(http.HandlerFunc(s.handleActive)))
	mux.Handle("GET /api/quests/offerable", authMW(http.HandlerFunc(s.handleOfferable)))
	mux.Handle("POST /api/quests/{id}/accept", authMW(http.HandlerFunc(s.handleAccept)))
	mux.Handle("POST /api/quests/{id}/abandon", authMW(http.HandlerFunc(s.handleAbandon)))
}

type offerDTO struct {
	QuestID    string `json:"questId"`
	Title      string `json:"title"`
	TotalSteps int    `json:"totalSteps"`
}

// handleOfferable lists the quests a player can accept (phase 8.17). Static
// catalog — the panel pairs it with the active list to show Accept buttons.
func (s *Server) handleOfferable(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.PlayerIDFromContext(r.Context()); !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	defs := Offerable()
	out := make([]offerDTO, 0, len(defs))
	for _, d := range defs {
		out = append(out, offerDTO{QuestID: d.ID, Title: d.Title, TotalSteps: len(d.Steps)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

type questDTO struct {
	QuestID      string `json:"questId"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	StepIndex    int    `json:"stepIndex"`
	TotalSteps   int    `json:"totalSteps"`
	StepDesc     string `json:"stepDesc"`
	StepReward   int64  `json:"stepReward"`
	StepGoal     int64  `json:"stepGoal"`
	StepProgress int64  `json:"stepProgress"`
	DeadlineUnix int64  `json:"deadlineUnix"` // 0 = no deadline
	Done         bool   `json:"done"`
	Failed       bool   `json:"failed"`
}

func (s *Server) handleActive(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	views, err := s.svc.ActiveList(r.Context(), player)
	if err != nil {
		s.logger.Error("quest: active", "err", err, "player", int64(player))
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	out := make([]questDTO, 0, len(views))
	for _, v := range views {
		dto := questDTO{
			QuestID: v.QuestID, Title: v.Title, Status: string(v.Status),
			StepIndex: v.StepIndex, TotalSteps: v.TotalSteps,
			StepDesc: v.StepDesc, StepReward: v.StepReward,
			StepGoal: v.StepGoal, StepProgress: v.StepProgress,
			Done: v.Done, Failed: v.Failed,
		}
		if !v.Deadline.IsZero() {
			dto.DeadlineUnix = v.Deadline.Unix()
		}
		out = append(out, dto)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	questID := r.PathValue("id")
	err := s.svc.Accept(r.Context(), player, questID)
	switch {
	case errors.Is(err, ErrNotOfferable):
		http.Error(w, `{"error":"quest not offerable"}`, http.StatusNotFound)
	case errors.Is(err, ErrPrerequisiteNotMet):
		http.Error(w, `{"error":"prerequisite not completed"}`, http.StatusConflict)
	case err != nil:
		s.logger.Error("quest: accept", "err", err, "player", int64(player), "quest", questID)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleAbandon(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	questID := r.PathValue("id")
	if err := s.svc.Abandon(r.Context(), player, questID); err != nil {
		s.logger.Error("quest: abandon", "err", err, "player", int64(player), "quest", questID)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

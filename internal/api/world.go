package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
)

func (s *Server) handleWorld(w http.ResponseWriter, _ *http.Request) {
	if s.topology == nil {
		writeError(w, http.StatusServiceUnavailable, "world not loaded")
		return
	}
	writeJSON(w, http.StatusOK, dto.WorldFromDomain(
		s.topology.Sectors(),
		s.topology.Gates(),
	))
}

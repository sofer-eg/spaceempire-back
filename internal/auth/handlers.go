package auth

import (
	"encoding/json"
	"errors"
	"net/http"
)

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	playerID, session, err := s.svc.Register(r.Context(), req.Login, req.Password, req.Race)
	if errors.Is(err, ErrLoginTaken) {
		writeError(w, http.StatusConflict, "login taken")
		return
	}
	if err != nil {
		s.logger.Error("register", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	s.setSessionCookie(w, session.Token)
	writeJSON(w, http.StatusOK, PlayerResponse{PlayerID: int64(playerID), Login: req.Login})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	player, session, err := s.svc.Login(r.Context(), req.Login, req.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		s.logger.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	s.setSessionCookie(w, session.Token)
	writeJSON(w, http.StatusOK, PlayerResponse{PlayerID: int64(player.ID), Login: player.Login})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err == nil {
		if dErr := s.svc.Logout(r.Context(), cookie.Value); dErr != nil {
			s.logger.Error("logout", "err", dErr)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	players, err := s.svc.ListPlayers(r.Context())
	if err != nil {
		s.logger.Error("players", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]PlayerResponse, len(players))
	for i, p := range players {
		out[i] = PlayerResponse{PlayerID: int64(p.ID), Login: p.Login}
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePlayerSelf is mounted by RegisterPlayerSelf. RequireAuth has
// already authenticated the request, so the player id lives on the
// context — we only need to fetch the login and the wallet balance.
func (s *Server) handlePlayerSelf(reader SelfReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		playerID, ok := PlayerIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		player, err := s.svc.GetByID(r.Context(), playerID)
		if err != nil {
			s.logger.Error("player self: lookup", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		cash, err := reader.GetCash(r.Context(), playerID)
		if err != nil {
			s.logger.Error("player self: cash", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		var activeShipID *int64
		if shipID, ok, err := reader.ActiveShip(r.Context(), playerID); err != nil {
			s.logger.Error("player self: active ship", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		} else if ok {
			id := int64(shipID)
			activeShipID = &id
		}
		var passengerOfShipID *int64
		if hostID, ok, err := reader.PassengerHost(r.Context(), playerID); err != nil {
			s.logger.Error("player self: passenger host", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		} else if ok {
			id := int64(hostID)
			passengerOfShipID = &id
		}
		writeJSON(w, http.StatusOK, PlayerSelfResponse{
			PlayerID:          int64(playerID),
			Login:             player.Login,
			Cash:              cash,
			ActiveShipID:      activeShipID,
			PassengerOfShipID: passengerOfShipID,
		})
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	player, err := s.svc.Authenticate(r.Context(), cookie.Value)
	if errors.Is(err, ErrNotAuthenticated) {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if err != nil {
		s.logger.Error("me", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, PlayerResponse{PlayerID: int64(player.ID), Login: player.Login})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   s.cfg.SessionTTLSeconds,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

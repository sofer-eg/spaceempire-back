package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// evaServer serves the EVA endpoints (phase 10.23): exit ship into a spacesuit,
// toggle ship access, board another ship (control own / ride others as a
// passenger), and disembark. It mirrors outfitServer/shipyardServer — an
// app-package HTTP server wired in app.go behind RequireAuth.
type evaServer struct {
	pool    evaLocator
	suits   spacesuitSpawner
	players evaPlayers
	bus     evaPublisher
	npc     domain.PlayerID
	cfg     EVAConfig
	logger  *slog.Logger
}

// EVAConfig holds the EVA tunables. BoardRange is how close (world units) a
// free-flying spacesuit must be to a ship to board it in space; AckTimeout caps
// the wait on a worker command.
type EVAConfig struct {
	BoardRange float64
	AckTimeout time.Duration
}

func (c EVAConfig) withDefaults() EVAConfig {
	if c.BoardRange <= 0 {
		c.BoardRange = 3
	}
	if c.AckTimeout <= 0 {
		c.AckTimeout = time.Second
	}
	return c
}

// evaLocator is the slice of *sector.Pool the EVA server needs.
type evaLocator interface {
	LookupShipSector(shipID domain.ShipID) (domain.SectorID, bool)
	LookupPrimaryShipByPlayer(player domain.PlayerID) (domain.ShipID, domain.SectorID, bool)
	Snapshot(sectorID domain.SectorID) sector.Snapshot
	Send(sectorID domain.SectorID, cmd sector.Command) error
}

// spacesuitSpawner spawns a spacesuit and returns its id. *shipSpawner satisfies it.
type spacesuitSpawner interface {
	SpawnSpacesuit(ctx context.Context, player domain.PlayerID, sectorID domain.SectorID, pos domain.Vec2, docked *domain.EntityRef) (domain.ShipID, error)
}

// evaPlayers is the slice of *players.Repository the EVA server needs.
type evaPlayers interface {
	ActiveShip(ctx context.Context, player domain.PlayerID) (domain.ShipID, bool, error)
	SetActiveShip(ctx context.Context, player domain.PlayerID, shipID domain.ShipID) error
	PassengerHost(ctx context.Context, player domain.PlayerID) (domain.ShipID, bool, error)
	SetPassengerHost(ctx context.Context, player domain.PlayerID, hostID domain.ShipID) error
}

// evaPublisher publishes the player-handoff event so the WS follows the player.
type evaPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

func newEvaServer(pool evaLocator, suits spacesuitSpawner, players evaPlayers, bus evaPublisher, npc domain.PlayerID, cfg EVAConfig, logger *slog.Logger) *evaServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &evaServer{pool: pool, suits: suits, players: players, bus: bus, npc: npc, cfg: cfg.withDefaults(), logger: logger}
}

// RegisterRoutes mounts the EVA endpoints behind auth.
//
//	POST /api/cmd/exit-ship
//	POST /api/cmd/ship-access
//	POST /api/cmd/board-ship
func (s *evaServer) RegisterRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/cmd/exit-ship", authMW(http.HandlerFunc(s.handleExitShip)))
	mux.Handle("POST /api/cmd/ship-access", authMW(http.HandlerFunc(s.handleShipAccess)))
	mux.Handle("POST /api/cmd/board-ship", authMW(http.HandlerFunc(s.handleBoardShip)))
	mux.Handle("POST /api/cmd/disembark", authMW(http.HandlerFunc(s.handleDisembark)))
}

type disembarkResponse struct {
	OK     bool  `json:"ok"`
	ShipID int64 `json:"shipID"` // the new spacesuit
}

// handleDisembark drops a passenger off their host into a spacesuit at the
// host's current spot (phase 10.23): docked at a station → the suit lands in
// that hangar; in space → it floats next to the host. Clears the passenger link
// and makes the suit the player's active ship.
func (s *evaServer) handleDisembark(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "не авторизован")
		return
	}
	hostID, riding, err := s.players.PassengerHost(r.Context(), player)
	if err != nil {
		s.logger.Error("disembark: passenger host", "err", err, "player", int64(player))
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}
	if !riding || hostID == 0 {
		writeJSONError(w, http.StatusConflict, "вы не пассажир")
		return
	}
	hostSec, ok := s.pool.LookupShipSector(hostID)
	if !ok {
		// Host vanished (e.g. destroyed) without the eject path clearing us.
		// Clear the dangling link so the player isn't stuck riding nothing.
		_ = s.players.SetPassengerHost(r.Context(), player, 0)
		writeJSONError(w, http.StatusConflict, "корабль-носитель недоступен")
		return
	}
	var host domain.Ship
	found := false
	for _, sh := range s.pool.Snapshot(hostSec).Ships {
		if sh.ID == hostID {
			host = sh
			found = true
			break
		}
	}
	if !found {
		_ = s.players.SetPassengerHost(r.Context(), player, 0)
		writeJSONError(w, http.StatusConflict, "корабль-носитель недоступен")
		return
	}

	suitID, err := s.suits.SpawnSpacesuit(r.Context(), player, host.SectorID, host.Pos, host.Docked)
	if err != nil {
		s.logger.Error("disembark: spawn suit", "err", err, "player", int64(player))
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}
	if err := s.players.SetPassengerHost(r.Context(), player, 0); err != nil {
		s.logger.Error("disembark: clear host", "err", err, "player", int64(player))
	}
	if err := s.players.SetActiveShip(r.Context(), player, suitID); err != nil {
		s.logger.Error("disembark: set active", "err", err, "player", int64(player))
	}
	s.mirrorPassengerRemove(hostSec, hostID, player)
	s.publishPlayerHandoff(r.Context(), player, suitID, host.SectorID)
	writeJSON(w, disembarkResponse{OK: true, ShipID: int64(suitID)})
}

func (s *evaServer) mirrorPassengerRemove(sec domain.SectorID, hostID domain.ShipID, player domain.PlayerID) {
	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sec, sector.RemovePassengerCommand{HostID: hostID, PlayerID: player, Reply: reply}); err != nil {
		s.logger.Error("eva: remove passenger", "err", err, "host", int64(hostID))
		return
	}
	s.waitAck(reply)
}

type shipAccessRequest struct {
	ShipID int64 `json:"shipID"`
	Open   bool  `json:"open"`
}

type shipAccessResponse struct {
	OK   bool `json:"ok"`
	Open bool `json:"open"`
}

// handleShipAccess toggles whether other players may board the caller's ship as
// a passenger (phase 10.23). Routed to the worker as SetShipAccessCommand so the
// flag mutates under the one-writer invariant and persists immediately.
func (s *evaServer) handleShipAccess(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "не авторизован")
		return
	}
	var req shipAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	sec, ok := s.pool.LookupShipSector(domain.ShipID(req.ShipID))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "корабль не найден")
		return
	}
	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sec, sector.SetShipAccessCommand{
		PlayerID: player, ShipID: domain.ShipID(req.ShipID), Open: req.Open, Reply: reply,
	}); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "сектор занят")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AckTimeout)
	defer cancel()
	select {
	case res := <-reply:
		switch {
		case errors.Is(res.Err, sector.ErrShipNotFound):
			writeJSONError(w, http.StatusNotFound, "корабль не найден")
		case errors.Is(res.Err, sector.ErrForbidden):
			writeJSONError(w, http.StatusForbidden, "чужой корабль")
		case res.Err != nil:
			s.logger.Error("ship-access", "err", res.Err, "player", int64(player), "ship", req.ShipID)
			writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		default:
			writeJSON(w, shipAccessResponse{OK: true, Open: req.Open})
		}
	case <-ctx.Done():
		writeJSONError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}

type exitShipRequest struct {
	ShipID int64 `json:"shipID"`
}

type exitShipResponse struct {
	OK     bool  `json:"ok"`
	ShipID int64 `json:"shipID"` // the new spacesuit
}

// handleExitShip drops the player out of their ship into a spacesuit at the
// ship's current spot (phase 10.23). Docked at a station → the suit stays docked
// there (the player is in the hangar); in space → the suit floats in space. The
// abandoned ship stays in the world (still owned). The spacesuit becomes the
// player's active ship; it is in the same sector, so the SPA only needs to
// refreshPlayer — no handoff.
func (s *evaServer) handleExitShip(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "не авторизован")
		return
	}
	var req exitShipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}

	ship, _, ok := s.findOwnedShip(w, player, domain.ShipID(req.ShipID))
	if !ok {
		return
	}
	if ship.IsSpacesuit {
		writeJSONError(w, http.StatusConflict, "вы уже в скафандре")
		return
	}

	suitID, err := s.suits.SpawnSpacesuit(r.Context(), player, ship.SectorID, ship.Pos, ship.Docked)
	if err != nil {
		s.logger.Error("exit-ship: spawn suit", "err", err, "player", int64(player), "ship", req.ShipID)
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}
	if err := s.players.SetActiveShip(r.Context(), player, suitID); err != nil {
		s.logger.Error("exit-ship: set active", "err", err, "player", int64(player), "suit", int64(suitID))
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}
	writeJSON(w, exitShipResponse{OK: true, ShipID: int64(suitID)})
}

type boardShipRequest struct {
	TargetShipID int64 `json:"targetShipID"`
}

type boardShipResponse struct {
	OK   bool   `json:"ok"`
	Mode string `json:"mode"` // "control" (own ship) | "passenger" (npc/open ship)
}

// handleBoardShip moves the player out of their spacesuit into a target ship
// (phase 10.23). Boarding their OWN ship → they take control (active_ship_id
// switches). Boarding an NPC ship or another player's OPEN ship → they ride as
// a passenger (no control). Requires the player to currently be in a spacesuit
// co-located with the target (same dock, or within BoardRange in space). The
// spacesuit is consumed; on disembark/host-death a fresh one is spawned.
func (s *evaServer) handleBoardShip(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "не авторизован")
		return
	}
	var req boardShipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}

	suit, suitSec, ok := s.resolveSuit(w, r.Context(), player)
	if !ok {
		return
	}
	targetID := domain.ShipID(req.TargetShipID)
	if targetID == suit.ID {
		writeJSONError(w, http.StatusBadRequest, "нельзя сесть в свой скафандр")
		return
	}
	targetSec, ok := s.pool.LookupShipSector(targetID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "корабль не найден")
		return
	}
	var target domain.Ship
	found := false
	for _, sh := range s.pool.Snapshot(targetSec).Ships {
		if sh.ID == targetID {
			target = sh
			found = true
			break
		}
	}
	if !found || targetSec != suitSec || !coLocated(suit, target, s.cfg.BoardRange) {
		writeJSONError(w, http.StatusConflict, "цель слишком далеко")
		return
	}

	switch {
	case target.PlayerID == player && !target.IsSpacesuit:
		s.boardOwn(r.Context(), player, suit, target, targetSec)
		writeJSON(w, boardShipResponse{OK: true, Mode: "control"})
	case target.PlayerID == s.npc || (target.PlayerID != player && target.IsOpen && !target.IsSpacesuit):
		s.boardAsPassenger(r.Context(), player, suit, target, targetSec)
		writeJSON(w, boardShipResponse{OK: true, Mode: "passenger"})
	case target.PlayerID != player && !target.IsOpen:
		writeJSONError(w, http.StatusForbidden, "вход на этот корабль закрыт")
	default:
		writeJSONError(w, http.StatusBadRequest, "на этот объект нельзя сесть")
	}
}

// boardOwn: take control of an owned ship. The spacesuit is removed; the target
// becomes the active ship; the WS follows it (same sector → the SPA refreshes).
func (s *evaServer) boardOwn(ctx context.Context, player domain.PlayerID, suit, target domain.Ship, targetSec domain.SectorID) {
	s.mirrorRemoveShip(suit.SectorID, suit.ID)
	if err := s.players.SetActiveShip(ctx, player, target.ID); err != nil {
		s.logger.Error("board-own: set active", "err", err, "player", int64(player))
	}
	if err := s.players.SetPassengerHost(ctx, player, 0); err != nil {
		s.logger.Error("board-own: clear passenger", "err", err, "player", int64(player))
	}
	s.publishPlayerHandoff(ctx, player, target.ID, targetSec)
}

// boardAsPassenger: ride a non-owned ship. The spacesuit is consumed; the player
// has no own ship while riding (active_ship_id NULL, passenger_of_ship_id set);
// the host's RAM PassengerPlayers is updated for jump/death fan-out; the WS
// follows the host.
func (s *evaServer) boardAsPassenger(ctx context.Context, player domain.PlayerID, suit, host domain.Ship, hostSec domain.SectorID) {
	s.mirrorRemoveShip(suit.SectorID, suit.ID)
	if err := s.players.SetActiveShip(ctx, player, 0); err != nil {
		s.logger.Error("board-passenger: clear active", "err", err, "player", int64(player))
	}
	if err := s.players.SetPassengerHost(ctx, player, host.ID); err != nil {
		s.logger.Error("board-passenger: set host", "err", err, "player", int64(player))
	}
	s.mirrorPassenger(hostSec, sector.AddPassengerCommand{HostID: host.ID, PlayerID: player})
	s.publishPlayerHandoff(ctx, player, host.ID, hostSec)
}

// resolveSuit returns the player's current spacesuit (their active ship) and its
// sector. Writes the HTTP error and returns ok=false when the player has no
// ship or is not currently in a spacesuit (they must exit first).
func (s *evaServer) resolveSuit(w http.ResponseWriter, ctx context.Context, player domain.PlayerID) (domain.Ship, domain.SectorID, bool) {
	suitID, has, err := s.players.ActiveShip(ctx, player)
	if err != nil || !has || suitID == 0 {
		if id, _, found := s.pool.LookupPrimaryShipByPlayer(player); found {
			suitID = id
		} else {
			writeJSONError(w, http.StatusConflict, "нет активного корабля")
			return domain.Ship{}, 0, false
		}
	}
	sec, ok := s.pool.LookupShipSector(suitID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "нет активного корабля")
		return domain.Ship{}, 0, false
	}
	for _, sh := range s.pool.Snapshot(sec).Ships {
		if sh.ID == suitID {
			if !sh.IsSpacesuit {
				writeJSONError(w, http.StatusConflict, "сначала выйдите из корабля")
				return domain.Ship{}, 0, false
			}
			return sh, sec, true
		}
	}
	writeJSONError(w, http.StatusConflict, "нет активного корабля")
	return domain.Ship{}, 0, false
}

// coLocated reports whether the spacesuit can board the target: both docked at
// the same static, or both free in space within rng.
func coLocated(a, b domain.Ship, rng float64) bool {
	if a.Docked != nil && b.Docked != nil {
		return *a.Docked == *b.Docked
	}
	if a.Docked == nil && b.Docked == nil {
		return a.Pos.Sub(b.Pos).Length() <= rng
	}
	return false
}

func (s *evaServer) mirrorRemoveShip(sec domain.SectorID, shipID domain.ShipID) {
	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sec, sector.RemoveShipCommand{ShipID: shipID, Reply: reply}); err != nil {
		s.logger.Error("eva: remove suit", "err", err, "ship", int64(shipID))
		return
	}
	s.waitAck(reply)
}

func (s *evaServer) mirrorPassenger(sec domain.SectorID, cmd sector.AddPassengerCommand) {
	reply := make(chan sector.CmdResult, 1)
	cmd.Reply = reply
	if err := s.pool.Send(sec, cmd); err != nil {
		s.logger.Error("eva: add passenger", "err", err, "host", int64(cmd.HostID))
		return
	}
	s.waitAck(reply)
}

func (s *evaServer) waitAck(reply <-chan sector.CmdResult) {
	select {
	case <-reply:
	case <-time.After(s.cfg.AckTimeout):
	}
}

// publishPlayerHandoff moves the player's WS to targetSec so the camera follows
// the ship they boarded. A same-sector hop is a no-op on the WS side (the SPA
// picks the new active/passenger state up from refreshPlayer).
func (s *evaServer) publishPlayerHandoff(ctx context.Context, player domain.PlayerID, shipID domain.ShipID, targetSec domain.SectorID) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(sector.PlayerHandoffEvent{PlayerID: player, ShipID: shipID, TargetSector: targetSec})
	if err != nil {
		s.logger.Warn("eva: marshal handoff", "err", err)
		return
	}
	if err := s.bus.Publish(ctx, sector.PlayerHandoffTopic(player), payload); err != nil {
		s.logger.Warn("eva: publish handoff", "err", err, "player", int64(player))
	}
}

// findOwnedShip locates a ship by id in its sector snapshot and asserts the
// caller owns it. Writes the HTTP error and returns ok=false on miss/forbidden.
func (s *evaServer) findOwnedShip(w http.ResponseWriter, player domain.PlayerID, shipID domain.ShipID) (domain.Ship, domain.SectorID, bool) {
	sec, ok := s.pool.LookupShipSector(shipID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "корабль не найден")
		return domain.Ship{}, 0, false
	}
	for _, sh := range s.pool.Snapshot(sec).Ships {
		if sh.ID == shipID {
			if sh.PlayerID != player {
				writeJSONError(w, http.StatusForbidden, "чужой корабль")
				return domain.Ship{}, 0, false
			}
			return sh, sec, true
		}
	}
	writeJSONError(w, http.StatusNotFound, "корабль не найден")
	return domain.Ship{}, 0, false
}

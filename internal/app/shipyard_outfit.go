package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
	shipsrepo "spaceempire/back/internal/persistence/ships"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/sector"
)

// errInsufficientCash is the in-tx sentinel the wallet debit returns so the
// handler can map it to 409 after the transaction rolls back.
var errInsufficientCash = errors.New("outfit: insufficient cash")

// outfitServer serves the shipyard buy-ship and equipment install/uninstall
// endpoints (phase 10.14). Buying spawns a fresh class ship docked at the
// shipyard (the player keeps flying their active ship — there is no active-ship
// switch yet); outfitting folds stat modules into the ship's ТТХ. See
// back/docs/specs/equipment_effects.md.
type outfitServer struct {
	pool       shipyardLocator
	ships      *shipsrepo.Repository
	players    *playersrepo.Repository
	tx         *database.TxManager
	classes    *balance.ShipClasses
	equipment  *balance.Equipments
	raceReader playerRaceReader
	cfg        ShipSpawnerConfig
	logger     *slog.Logger
}

func newOutfitServer(pool shipyardLocator, ships *shipsrepo.Repository, players *playersrepo.Repository, tx *database.TxManager, classes *balance.ShipClasses, equipment *balance.Equipments, raceReader playerRaceReader, cfg ShipSpawnerConfig, logger *slog.Logger) *outfitServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &outfitServer{
		pool:       pool,
		ships:      ships,
		players:    players,
		tx:         tx,
		classes:    classes,
		equipment:  equipment,
		raceReader: raceReader,
		cfg:        cfg.withDefaults(),
		logger:     logger,
	}
}

// RegisterRoutes mounts the buy / install / uninstall endpoints behind auth.
//
//	POST /api/shipyard/{id}/buy-ship
//	POST /api/shipyard/{id}/install-equipment
//	POST /api/shipyard/{id}/uninstall-equipment
func (s *outfitServer) RegisterRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/shipyard/{id}/buy-ship", authMW(http.HandlerFunc(s.handleBuyShip)))
	mux.Handle("POST /api/shipyard/{id}/sell-ship", authMW(http.HandlerFunc(s.handleSellShip)))
	mux.Handle("POST /api/shipyard/{id}/install-equipment", authMW(http.HandlerFunc(s.handleInstall)))
	mux.Handle("POST /api/shipyard/{id}/uninstall-equipment", authMW(http.HandlerFunc(s.handleUninstall)))
}

type buyShipRequest struct {
	ClassID int64 `json:"classID"`
}

type buyShipResponse struct {
	OK     bool  `json:"ok"`
	ShipID int64 `json:"shipID"`
	Cash   int64 `json:"cash"`
}

func (s *outfitServer) handleBuyShip(w http.ResponseWriter, r *http.Request) {
	player, shipyardID, ok := s.authAndShipyard(w, r)
	if !ok {
		return
	}
	var req buyShipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	cls, ok := s.classes.GetShipClass(domain.ShipClassID(req.ClassID))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "неизвестный класс корабля")
		return
	}

	active, sectorID, ok := s.activeDockedShip(w, player, shipyardID)
	if !ok {
		return
	}

	race := s.playerRace(r.Context(), player)
	ship := s.buildPurchasedShip(player, cls, race, sectorID, active.Pos, shipyardID)

	var newCash int64
	var newID domain.ShipID
	err := s.tx.Do(r.Context(), func(ctx context.Context, txx pgx.Tx) error {
		cash, err := s.players.WithExecutor(txx).AdjustCash(ctx, player, -cls.BasePrice)
		if err != nil {
			if errors.Is(err, playersrepo.ErrInsufficientCash) {
				return errInsufficientCash
			}
			return err
		}
		newCash = cash
		id, err := s.ships.WithExecutor(txx).Create(ctx, ship)
		if err != nil {
			return err
		}
		newID = id
		return nil
	})
	if err != nil {
		if errors.Is(err, errInsufficientCash) {
			writeJSONError(w, http.StatusConflict, "недостаточно кредитов")
			return
		}
		s.logger.Error("buy-ship: tx", "err", err, "player", int64(player), "class", req.ClassID)
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}

	ship.ID = newID
	s.mirrorAddShip(sectorID, ship)
	writeJSON(w, buyShipResponse{OK: true, ShipID: int64(newID), Cash: newCash})
}

// buildPurchasedShip assembles a fresh ship of the given class, docked at the
// shipyard. Stats come from baseShipStats (class + spawn config); the ship
// starts with no equipment, so its effective stats equal the baseline.
func (s *outfitServer) buildPurchasedShip(player domain.PlayerID, cls balance.ShipClass, race domain.RaceID, sectorID domain.SectorID, pos domain.Vec2, shipyardID int64) domain.Ship {
	base := baseShipStats(cls, s.cfg)
	hp := cls.Hull
	if hp <= 0 {
		hp = s.cfg.StartHP
	}
	return domain.Ship{
		PlayerID:        player,
		Race:            race,
		Name:            cls.Name,
		ShipClassID:     cls.ID,
		SectorID:        sectorID,
		Pos:             pos,
		Direction:       domain.Vec2{X: 1, Y: 0},
		MaxSpeed:        base.MaxSpeed,
		Acceleration:    base.Acceleration,
		TurnRate:        s.cfg.StartTurnRate,
		HP:              hp,
		MaxHP:           hp,
		Shield:          base.MaxShield,
		MaxShield:       base.MaxShield,
		ShieldRecharge:  base.ShieldRecharge,
		Energy:          base.MaxEnergy,
		MaxEnergy:       base.MaxEnergy,
		EnergyRecharge:  base.EnergyRecharge,
		LaserDamage:     base.LaserDamage,
		LaserRange:      s.cfg.StartLaserRange,
		LaserEnergyCost: s.cfg.StartLaserECost,
		RadarRange:      base.RadarRange, // phase 10.20: bought ships get the class radar too
		Docked:          &domain.EntityRef{Kind: domain.EntityKindShipyard, ID: shipyardID},
	}
}

type installRequest struct {
	ShipID      int64 `json:"shipID"`
	EquipmentID int64 `json:"equipmentID"`
	Level       int   `json:"level"`
}

type outfitResponse struct {
	OK        bool                     `json:"ok"`
	Cash      int64                    `json:"cash"`
	Equipment []installedEquipmentResp `json:"equipment"`
}

func (s *outfitServer) handleInstall(w http.ResponseWriter, r *http.Request) {
	player, shipyardID, ok := s.authAndShipyard(w, r)
	if !ok {
		return
	}
	var req installRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}

	target, ok := s.ownedDockedShip(w, player, shipyardID, domain.ShipID(req.ShipID))
	if !ok {
		return
	}
	cls, ok := s.classes.GetShipClass(target.ShipClassID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "у корабля нет класса")
		return
	}

	rep, err := s.players.GetReputation(r.Context(), player)
	if err != nil {
		s.logger.Error("install-equipment: reputation", "err", err, "player", int64(player))
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}

	row, err := s.equipment.ResolveInstall(domain.EquipmentID(req.EquipmentID), cls.Class, int(target.Race), req.Level, target.Equipment,
		balance.Reputation{War: rep.War, Trade: rep.Trade, Race: rep.Race})
	if err != nil {
		writeBalanceError(w, err)
		return
	}

	level := req.Level
	if level < 1 {
		level = 1
	}
	newEquip := append(cloneInstalled(target.Equipment), domain.InstalledEquipment{
		EquipmentID: row.ID, Type: row.Type, Level: level,
	})
	eff := balance.ApplyEquipmentEffects(baseShipStats(cls, s.cfg), newEquip)
	price := balance.InstallPrice(row, level)

	newCash, err := s.persistOutfit(r.Context(), player, target.ID, newEquip, eff, price)
	if err != nil {
		if errors.Is(err, errInsufficientCash) {
			writeJSONError(w, http.StatusConflict, "недостаточно кредитов")
			return
		}
		s.logger.Error("install-equipment: persist", "err", err, "player", int64(player), "ship", req.ShipID)
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}
	writeJSON(w, outfitResponse{OK: true, Cash: newCash, Equipment: toInstalledResp(newEquip)})
}

type uninstallRequest struct {
	ShipID      int64 `json:"shipID"`
	EquipmentID int64 `json:"equipmentID"`
}

func (s *outfitServer) handleUninstall(w http.ResponseWriter, r *http.Request) {
	player, shipyardID, ok := s.authAndShipyard(w, r)
	if !ok {
		return
	}
	var req uninstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}

	target, ok := s.ownedDockedShip(w, player, shipyardID, domain.ShipID(req.ShipID))
	if !ok {
		return
	}
	cls, ok := s.classes.GetShipClass(target.ShipClassID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "у корабля нет класса")
		return
	}

	removed, newEquip, found := removeEquipment(target.Equipment, domain.EquipmentID(req.EquipmentID))
	if !found {
		writeBalanceError(w, balance.ErrEquipmentNotInstalled)
		return
	}
	if s.isDependedOn(removed.Type, newEquip) {
		writeJSONError(w, http.StatusConflict, "модуль используется другим оборудованием")
		return
	}

	eff := balance.ApplyEquipmentEffects(baseShipStats(cls, s.cfg), newEquip)
	updated := outfitShip(target.ID, newEquip, eff)
	if err := s.ships.SaveEquipment(r.Context(), updated); err != nil {
		s.logger.Error("uninstall-equipment: save", "err", err, "player", int64(player), "ship", req.ShipID)
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}
	s.mirrorEquipment(player, target.ID, newEquip, eff)

	cash, _ := s.players.GetCash(r.Context(), player)
	writeJSON(w, outfitResponse{OK: true, Cash: cash, Equipment: toInstalledResp(newEquip)})
}

// persistOutfit debits price and saves the new equipment + folded stats in one
// transaction, then mirrors the change into the worker's RAM copy. Returns the
// new cash balance, or errInsufficientCash.
func (s *outfitServer) persistOutfit(ctx context.Context, player domain.PlayerID, shipID domain.ShipID, eq []domain.InstalledEquipment, eff balance.ShipStats, price int64) (int64, error) {
	var newCash int64
	err := s.tx.Do(ctx, func(ctx context.Context, txx pgx.Tx) error {
		cash, err := s.players.WithExecutor(txx).AdjustCash(ctx, player, -price)
		if err != nil {
			if errors.Is(err, playersrepo.ErrInsufficientCash) {
				return errInsufficientCash
			}
			return err
		}
		newCash = cash
		return s.ships.WithExecutor(txx).SaveEquipment(ctx, outfitShip(shipID, eq, eff))
	})
	if err != nil {
		return 0, err
	}
	s.mirrorEquipment(player, shipID, eq, eff)
	return newCash, nil
}

// --- helpers ---------------------------------------------------------------

func (s *outfitServer) authAndShipyard(w http.ResponseWriter, r *http.Request) (domain.PlayerID, int64, bool) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "не авторизован")
		return 0, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "некорректный id верфи")
		return 0, 0, false
	}
	return player, id, true
}

// activeDockedShip returns the player's active (primary) ship and its sector,
// asserting it is docked at this shipyard. Used as the buy guard and to locate
// the shipyard's sector/spawn position.
func (s *outfitServer) activeDockedShip(w http.ResponseWriter, player domain.PlayerID, shipyardID int64) (domain.Ship, domain.SectorID, bool) {
	shipID, sectorID, found := s.pool.LookupPrimaryShipByPlayer(player)
	if !found {
		writeJSONError(w, http.StatusConflict, "нет активного корабля")
		return domain.Ship{}, 0, false
	}
	snap := s.pool.Snapshot(sectorID)
	for i := range snap.Ships {
		if snap.Ships[i].ID == shipID {
			ship := snap.Ships[i]
			if !dockedAtShipyard(ship.Docked, shipyardID) {
				writeJSONError(w, http.StatusConflict, "пристыкуйтесь к этой верфи")
				return domain.Ship{}, 0, false
			}
			return ship, sectorID, true
		}
	}
	writeJSONError(w, http.StatusConflict, "корабль не найден")
	return domain.Ship{}, 0, false
}

// ownedDockedShip locates an arbitrary ship the player owns that is docked at
// this shipyard (the active ship or one bought earlier). It searches the
// player's active-ship sector — every ship docked at the shipyard shares it.
func (s *outfitServer) ownedDockedShip(w http.ResponseWriter, player domain.PlayerID, shipyardID int64, shipID domain.ShipID) (domain.Ship, bool) {
	_, sectorID, found := s.pool.LookupPrimaryShipByPlayer(player)
	if !found {
		writeJSONError(w, http.StatusConflict, "нет активного корабля")
		return domain.Ship{}, false
	}
	snap := s.pool.Snapshot(sectorID)
	for i := range snap.Ships {
		if snap.Ships[i].ID != shipID {
			continue
		}
		ship := snap.Ships[i]
		if ship.PlayerID != player {
			writeJSONError(w, http.StatusForbidden, "чужой корабль")
			return domain.Ship{}, false
		}
		if !dockedAtShipyard(ship.Docked, shipyardID) {
			writeJSONError(w, http.StatusConflict, "пристыкуйте корабль к этой верфи")
			return domain.Ship{}, false
		}
		return ship, true
	}
	writeJSONError(w, http.StatusConflict, "корабль не найден на этой верфи")
	return domain.Ship{}, false
}

func dockedAtShipyard(docked *domain.EntityRef, shipyardID int64) bool {
	return docked != nil && docked.Kind == domain.EntityKindShipyard && docked.ID == shipyardID
}

// playerRace reads the player's chosen race (0 when no reader is wired).
func (s *outfitServer) playerRace(ctx context.Context, player domain.PlayerID) domain.RaceID {
	if s.raceReader == nil {
		return 0
	}
	race, err := s.raceReader.PlayerRace(ctx, player)
	if err != nil {
		s.logger.Error("outfit: read player race", "err", err, "player", int64(player))
		return 0
	}
	return race
}

// mirrorAddShip registers a freshly purchased ship into the worker's RAM state.
func (s *outfitServer) mirrorAddShip(sectorID domain.SectorID, ship domain.Ship) {
	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sectorID, sector.AddShipCommand{Ship: ship, Reply: reply}); err != nil {
		s.logger.Error("buy-ship: mirror add", "err", err, "ship", int64(ship.ID), "sector", int64(sectorID))
		return
	}
	select {
	case <-reply:
	case <-time.After(s.cfg.AckTimeout):
	}
}

// mirrorEquipment pushes the recomputed fit into the worker's RAM ship.
func (s *outfitServer) mirrorEquipment(player domain.PlayerID, shipID domain.ShipID, eq []domain.InstalledEquipment, eff balance.ShipStats) {
	reply := make(chan sector.CmdResult, 1)
	_, sectorID, found := s.pool.LookupPrimaryShipByPlayer(player)
	if !found {
		return
	}
	cmd := sector.UpdateShipEquipmentCommand{
		PlayerID:       player,
		ShipID:         shipID,
		Equipment:      eq,
		MaxSpeed:       eff.MaxSpeed,
		Acceleration:   eff.Acceleration,
		MaxShield:      eff.MaxShield,
		ShieldRecharge: eff.ShieldRecharge,
		MaxEnergy:      eff.MaxEnergy,
		EnergyRecharge: eff.EnergyRecharge,
		LaserDamage:    eff.LaserDamage,
		RadarRange:     eff.RadarRange,
		Reply:          reply,
	}
	if err := s.pool.Send(sectorID, cmd); err != nil {
		s.logger.Error("outfit: mirror equipment", "err", err, "ship", int64(shipID), "sector", int64(sectorID))
		return
	}
	select {
	case <-reply:
	case <-time.After(s.cfg.AckTimeout):
	}
}

// isDependedOn reports whether any still-installed module declares typ as its
// catalog Dependance — blocking removal of a prerequisite still in use.
func (s *outfitServer) isDependedOn(typ string, installed []domain.InstalledEquipment) bool {
	for _, m := range installed {
		row, ok := s.equipment.GetEquipment(m.EquipmentID)
		if ok && row.Dependance == typ {
			return true
		}
	}
	return false
}

// outfitShip builds the minimal domain.Ship SaveEquipment / the RAM command
// read: the id, the new equipment list and the folded stat fields.
func outfitShip(id domain.ShipID, eq []domain.InstalledEquipment, eff balance.ShipStats) domain.Ship {
	return domain.Ship{
		ID:             id,
		Equipment:      eq,
		MaxSpeed:       eff.MaxSpeed,
		Acceleration:   eff.Acceleration,
		MaxShield:      eff.MaxShield,
		ShieldRecharge: eff.ShieldRecharge,
		MaxEnergy:      eff.MaxEnergy,
		EnergyRecharge: eff.EnergyRecharge,
		LaserDamage:    eff.LaserDamage,
		RadarRange:     eff.RadarRange,
	}
}

func cloneInstalled(eq []domain.InstalledEquipment) []domain.InstalledEquipment {
	if len(eq) == 0 {
		return nil
	}
	return append([]domain.InstalledEquipment(nil), eq...)
}

// removeEquipment returns the removed entry, the list without it, and whether
// a module with that id was present.
func removeEquipment(eq []domain.InstalledEquipment, id domain.EquipmentID) (domain.InstalledEquipment, []domain.InstalledEquipment, bool) {
	out := make([]domain.InstalledEquipment, 0, len(eq))
	var removed domain.InstalledEquipment
	found := false
	for _, m := range eq {
		if m.EquipmentID == id {
			removed = m
			found = true
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		out = nil
	}
	return removed, out, found
}

type installedEquipmentResp struct {
	EquipmentID int64  `json:"equipmentID"`
	Type        string `json:"type"`
	Level       int    `json:"level"`
}

func toInstalledResp(eq []domain.InstalledEquipment) []installedEquipmentResp {
	out := make([]installedEquipmentResp, len(eq))
	for i, m := range eq {
		out[i] = installedEquipmentResp{EquipmentID: int64(m.EquipmentID), Type: m.Type, Level: m.Level}
	}
	return out
}

// writeBalanceError maps a balance install/uninstall sentinel to an HTTP code.
func writeBalanceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, balance.ErrEquipmentNotFound):
		writeJSONError(w, http.StatusNotFound, "оборудование не найдено")
	case errors.Is(err, balance.ErrEquipmentLevel):
		writeJSONError(w, http.StatusBadRequest, "недопустимый уровень оборудования")
	case errors.Is(err, balance.ErrEquipmentWrongClass):
		writeJSONError(w, http.StatusConflict, "оборудование недоступно для этого класса корабля")
	case errors.Is(err, balance.ErrEquipmentWrongRace):
		writeJSONError(w, http.StatusConflict, "оборудование недоступно для вашей расы")
	case errors.Is(err, balance.ErrEquipmentDependency):
		writeJSONError(w, http.StatusConflict, "сначала установите модуль, от которого зависит этот")
	case errors.Is(err, balance.ErrEquipmentAlreadyInstalled):
		writeJSONError(w, http.StatusConflict, "модуль этого типа уже установлен")
	case errors.Is(err, balance.ErrEquipmentNotInstalled):
		writeJSONError(w, http.StatusConflict, "модуль этого типа не установлен")
	case errors.Is(err, balance.ErrRankTooLow):
		writeJSONError(w, http.StatusUnprocessableEntity, "недостаточный ранг для этого оборудования")
	default:
		writeJSONError(w, http.StatusBadRequest, "не удалось установить оборудование")
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

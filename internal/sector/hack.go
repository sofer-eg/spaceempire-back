package sector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
)

const hackModuleType = "up_hack"

// pirateRace is race 6 in the StarWind dump — a pirate trade station cannot be
// hacked (its defence is "non-standard", SP UseHack). Kept local so the sector
// package stays free of the race reference dependency.
const pirateRace = 6

var (
	// ErrHackOutOfRange is reported by HackStationCommand when the hacker ship is
	// farther than HackRange from the trade station. HTTP maps it to 422.
	ErrHackOutOfRange = errors.New("sector: hack target out of range")
	// ErrHackTooLittleGoods is reported when the station's richest good holds
	// less than the min fraction of its max_stock — "too little goods to fool the
	// system" (SP UseHack). Returned by the StationRobber; HTTP maps it to 422.
	ErrHackTooLittleGoods = errors.New("sector: station has too little goods to hack")
)

// StationRobber runs the market + reputation side of a station hack
// (TASK-100.3.9.3, SP UseHack). It robs the station's richest good (deduct
// stock, deposit loot into the hacker's hold in one transaction) and drops the
// hacker's standing with the station's race proportionally to the fraction
// taken. Returns what was taken so the worker drops a loot container when the
// hold could not take it, and journals the outcome. Injected via
// WithStationRobber; nil disables the effect (pure unit tests). The real
// implementation lives in app/ over trade.Service + racestanding, keeping the
// sector package free of trade/standing deps — the same split as PoliceScanner.
type StationRobber interface {
	Rob(ctx context.Context, station domain.EntityRef, stationRace domain.RaceID,
		hacker domain.PlayerID, hackerShip domain.EntityRef, depositToHold bool) (RobResult, error)
}

// RobResult reports the loot outcome of a hack for the worker to finish (drop a
// container, journal). Robbed is the loot amount, Damaged the destroyed amount.
// Delivered is true when Robbed was already added to the hold (level >= 2 with
// room); false means the worker must drop a container with Robbed units.
type RobResult struct {
	GoodsType domain.GoodsTypeID
	Robbed    int64
	Damaged   int64
	Delivered bool
}

// WithStationRobber wires the station-hack executor (TASK-100.3.9.3). Nil
// disables HackStationCommand's effect.
func WithStationRobber(r StationRobber) Option {
	return func(w *Worker) {
		w.robber = r
	}
}

// StationHackedTopic is the per-player bus topic a station hack is published to
// (TASK-100.3.9.3). The WS handler subscribes to its own player's topic,
// mirroring PoliceScanTopic / ModuleKnockedTopic.
func StationHackedTopic(player domain.PlayerID) string {
	return fmt.Sprintf("station.hacked.%d", int64(player))
}

// StationHackedEvent is broadcast to the hacker when they raid a trade station.
// Robbed > 0 → "Похищено N ед."; Robbed == 0 → "Неудачная попытка взлома" (only
// the damage landed). The SPA renders the journal line (TASK-100.3.9.6).
type StationHackedEvent struct {
	PlayerID  domain.PlayerID    `json:"playerId"`
	ShipID    domain.ShipID      `json:"shipId"`
	SectorID  domain.SectorID    `json:"sectorId"`
	StationID int64              `json:"stationId"`
	Race      domain.RaceID      `json:"race"`
	GoodsType domain.GoodsTypeID `json:"goodsType"`
	Robbed    int64              `json:"robbed"`
}

// HackResult carries the loot amount back to the HTTP handler so the response
// can echo it. On error Robbed is zero and Err is non-nil.
type HackResult struct {
	Err    error
	Robbed int64
}

// HackStationCommand raids a trade station with the up_hack module (SP UseHack,
// ЧТЗ FR-D1..D5): it steals a fraction of the station's richest good into the
// hacker's hold (or a container), destroys another fraction, drops the hacker's
// standing with the station's race, and reveals the hacker for this tick.
// Ownership + the up_hack module + a valid, built, non-pirate trade station in
// range + energy are all gated. EnergyCost is the up_hack action energy the
// HTTP handler resolves from the catalog; the raid is rejected with
// ErrNotEnoughEnergy below the cost and debits it on success.
type HackStationCommand struct {
	PlayerID   domain.PlayerID
	ShipID     domain.ShipID
	Target     domain.EntityRef
	EnergyCost int
	Reply      chan<- HackResult
}

func (c HackStationCommand) apply(w *Worker, s *sectorState) {
	var res HackResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case shipEquipmentLevel(ship, hackModuleType) < 1:
		res.Err = ErrEquipmentRequired
	case !isHackableStationKind(c.Target.Kind):
		res.Err = ErrInvalidAttackTarget
	}
	if res.Err != nil {
		replyHack(c.Reply, res)
		return
	}

	// The market layer (station_goods) is owner-generic: both a trade station
	// (owner_kind 4) and a production factory (owner_kind 2) hold goods, so a hack
	// targets either (scope-extension over ЧТЗ C-07 — factories carry the real
	// stock; trade-station resale markets are near-empty on live data).
	station, ok := resolveHackStation(s, c.Target)
	switch {
	case !ok:
		res.Err = ErrInvalidAttackTarget // not in this sector
	case !station.built:
		res.Err = ErrInvalidAttackTarget
	case station.race == pirateRace:
		// A pirate station uses a non-standard defence — unhackable (SP UseHack).
		res.Err = ErrInvalidAttackTarget
	case ship.Pos.Sub(station.pos).Length() > w.cfg.HackRange:
		res.Err = ErrHackOutOfRange
	case ship.Energy < c.EnergyCost:
		// Availability check only — the energy is debited after the rob succeeds,
		// so a "too little goods" reject spends nothing.
		res.Err = ErrNotEnoughEnergy
	}
	if res.Err != nil {
		replyHack(c.Reply, res)
		return
	}

	// No robber wired (pure unit tests without app deps): the gates hold but the
	// effect is a no-op — mirror the miner/trader logistics nil-disables split.
	if w.robber == nil {
		replyHack(c.Reply, res)
		return
	}

	depositToHold := shipEquipmentLevel(ship, hackModuleType) >= 2
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)}
	// Commit first, mutate RAM after (TASK-152's ordering, TASK-148's deadline):
	// the stock deduction, the hold deposit and the standing drop are one
	// transaction, and the energy debit / container drop / journal below only run
	// once it has landed.
	var rob RobResult
	err := w.dbCall(context.Background(), func(ctx context.Context) error {
		var err error
		rob, err = w.robber.Rob(ctx, c.Target, domain.RaceID(station.race),
			c.PlayerID, shipRef, depositToHold)
		return err
	})
	if err != nil {
		// A depleted station is a clean reject (no energy spent); anything else is
		// surfaced as-is so the handler maps it (500 by default).
		res.Err = err
		if !errors.Is(err, ErrHackTooLittleGoods) {
			w.logRobError(err, s, ship, c.Target)
		}
		replyHack(c.Reply, res)
		return
	}

	// The hack committed: spend the action energy, reveal the hacker for this
	// tick (up_hide off, TASK-106 — same transient path as a missile launch), and
	// drop a loot container when the hold did not take the goods.
	if c.EnergyCost > 0 {
		ship.Energy -= c.EnergyCost
		s.markDirty(c.ShipID)
	}
	if ship.IsHidden {
		ship.MissileJustFired = true // reveal for this tick's snapshot (phase 10.20a)
	}
	if rob.Robbed > 0 && !rob.Delivered {
		w.spawnHackContainer(context.Background(), s, station.pos, rob.GoodsType, rob.Robbed)
	}
	w.publishStationHacked(context.Background(), s, StationHackedEvent{
		PlayerID:  c.PlayerID,
		ShipID:    ship.ID,
		SectorID:  s.sectorID,
		StationID: c.Target.ID,
		Race:      domain.RaceID(station.race),
		GoodsType: rob.GoodsType,
		Robbed:    rob.Robbed,
	})
	res.Robbed = rob.Robbed
	replyHack(c.Reply, res)
}

// logRobError records a failed station raid (TASK-148). The rob is one
// transaction — stock down, loot into the hold (level 2), standing down — so a
// clean failure leaves the station and the hacker exactly as they were: WARN,
// the player has the reason on the ack.
//
// A deadline is the one outcome that can cost goods, and unlike Pickup/Haul the
// ordering cannot fix it: the loot's destination for a level-1 hack is a
// container this worker spawns AFTER the rob returns, so a COMMIT that lands
// after the deadline fired takes the goods off the station's shelf with nothing
// created to hold them. They are gone. That window is inherent to the two-step
// rob-then-drop design and predates the deadline (a successful rob followed by a
// failed SpawnContainer loses the same goods); closing it needs the drop to ride
// the rob's transaction. Until then it is an ERROR carrying the station, ship and
// player so the amount can be reconstructed from the station's stock history.
func (w *Worker) logRobError(err error, s *sectorState, ship *domain.Ship, target domain.EntityRef) {
	if dbDeadline(err) {
		w.logger.Error("hack outcome in doubt: station stock may be deducted with the loot never delivered",
			"err", err, "ship", int64(ship.ID), "player", int64(ship.PlayerID),
			"station_kind", target.Kind, "station", target.ID,
			"sector", int64(s.sectorID), "repo_timeout", w.cfg.RepoTimeout)
		return
	}
	w.logger.Error("hack rob failed",
		"err", err, "ship", int64(ship.ID), "station", target.ID,
		"sector", int64(s.sectorID))
}

// replyHack delivers a HackResult on a buffered reply channel, never blocking.
func replyHack(reply chan<- HackResult, res HackResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}

// hackTarget is the normalised view of a hackable station (a trade station or a
// production factory) the command needs for its gates and effect.
type hackTarget struct {
	pos   domain.Vec2
	race  int
	built bool
}

// isHackableStationKind reports whether ref.Kind names a station whose goods can
// be raided: a trade station (owner_kind 4) or a production factory (owner_kind
// 2). Both back a station_goods market; other kinds are not hackable.
func isHackableStationKind(k domain.EntityKind) bool {
	return k == domain.EntityKindTradeStation || k == domain.EntityKindStation
}

// resolveHackStation resolves a hack target by id in the sector's statics,
// dispatching on kind: a trade station from TradeStations, a production factory
// from Stations. ok is false when the id is not present as the given kind here.
func resolveHackStation(s *sectorState, ref domain.EntityRef) (hackTarget, bool) {
	switch ref.Kind {
	case domain.EntityKindTradeStation:
		for i := range s.statics.TradeStations {
			if t := s.statics.TradeStations[i]; int64(t.ID) == ref.ID {
				return hackTarget{pos: t.Pos, race: t.Race, built: t.Built}, true
			}
		}
	case domain.EntityKindStation:
		for i := range s.statics.Stations {
			if st := s.statics.Stations[i]; int64(st.ID) == ref.ID {
				return hackTarget{pos: st.Pos, race: st.Race, built: st.Built}, true
			}
		}
	}
	return hackTarget{}, false
}

// spawnHackContainer drops the robbed goods as a loot container next to the
// station (level-1 loot, or the level-2 full-hold fallback). Reuses the loot
// container machinery (SpawnContainer); a nil repo (unit tests) is a no-op.
// Mirrors spawnOreContainer.
func (w *Worker) spawnHackContainer(ctx context.Context, s *sectorState, at domain.Vec2, gtype domain.GoodsTypeID, qty int64) {
	if w.containerRepo == nil {
		return
	}
	var c domain.Container
	err := w.dbCall(ctx, func(ctx context.Context) error {
		var err error
		c, err = w.containerRepo.SpawnContainer(ctx, s.sectorID, domain.ContainerDrop{
			Pos:       containerDropPos(at, 1, 2), // off-centre so it is not on the station
			ExpiresAt: w.clock.Now().Add(w.cfg.ContainerTTL),
			GoodsType: gtype,
			Quantity:  qty,
		})
		return err
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "hack spawn loot container failed",
			"err", err, "goods", int(gtype), "amount", qty, "sector", int64(s.sectorID))
		return
	}
	s.addContainer(&c)
}

// publishStationHacked emits the per-player hack event on the bus. Best-effort:
// a nil bus (pure unit tests) or a publish error is logged, never blocking the
// tick. Mirrors publishPoliceScan / publishModuleKnocked.
func (w *Worker) publishStationHacked(ctx context.Context, s *sectorState, ev StationHackedEvent) {
	if w.bus == nil || ev.PlayerID == 0 {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		w.logger.ErrorContext(ctx, "hack: marshal event", "err", err, "player", int64(ev.PlayerID))
		return
	}
	if err := w.publish(ctx, StationHackedTopic(ev.PlayerID), payload); err != nil {
		w.logger.ErrorContext(ctx, "hack: publish event", "err", err, "player", int64(ev.PlayerID))
	}
}

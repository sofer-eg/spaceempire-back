package sector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
// stock, deposit the loot into the hacker's hold, or create the loot container
// when the hold cannot take it — all in one transaction) and drops the hacker's
// standing with the station's race proportionally to the fraction taken. Returns
// what was taken so the worker can add the container to the live set and journal
// the outcome. Injected via WithStationRobber; nil disables the effect (pure unit
// tests). The real implementation lives in app/ over trade + containers +
// racestanding, keeping the sector package free of those deps — the same split as
// PoliceScanner.
type StationRobber interface {
	Rob(ctx context.Context, station domain.EntityRef, stationRace domain.RaceID,
		hacker domain.PlayerID, hackerShip domain.EntityRef, depositToHold bool,
		loot LootDrop) (RobResult, error)
}

// LootDrop is where the robber must put the loot container when the hold does not
// take the goods. The worker owns these values (its sector, a position off the
// station's centre, its container TTL) but not the write: the container INSERT
// rides the rob's transaction (TASK-160), so the robber needs them up front.
type LootDrop struct {
	SectorID  domain.SectorID
	Pos       domain.Vec2
	ExpiresAt time.Time
}

// RobResult reports the loot outcome of a hack for the worker to finish (add the
// container to the live set, journal). Robbed is the loot amount, Damaged the
// destroyed amount. Delivered is true when Robbed went to the hold (level >= 2
// with room). Container is non-nil when the robber created a loot container
// instead, in the same transaction as the stock deduction — the worker only has
// to add it to RAM.
type RobResult struct {
	GoodsType domain.GoodsTypeID
	Robbed    int64
	Damaged   int64
	Delivered bool
	Container *domain.Container
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
	// the stock deduction, the hold deposit / loot container and the standing drop
	// are one transaction, and the energy debit / journal below only run once it
	// has landed.
	loot := LootDrop{
		SectorID:  s.sectorID,
		Pos:       containerDropPos(station.pos, 1, 2), // off-centre so it is not on the station
		ExpiresAt: w.clock.Now().Add(w.cfg.ContainerTTL),
	}
	var rob RobResult
	err := w.dbCall(context.Background(), func(ctx context.Context) error {
		var err error
		rob, err = w.robber.Rob(ctx, c.Target, domain.RaceID(station.race),
			c.PlayerID, shipRef, depositToHold, loot)
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
	// publish the loot container the robber created in the same transaction.
	if c.EnergyCost > 0 {
		ship.Energy -= c.EnergyCost
		s.markDirty(c.ShipID)
	}
	if ship.IsHidden {
		ship.MissileJustFired = true // reveal for this tick's snapshot (phase 10.20a)
	}
	if rob.Container != nil {
		s.addContainer(rob.Container)
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
// transaction — stock down, loot into the hold (level 2) or into a freshly
// created container (level 1), standing down — so a clean failure leaves the
// station and the hacker exactly as they were: the player has the reason on the
// ack.
//
// A deadline stays the ambiguous outcome, but since TASK-160 it no longer costs
// goods: a COMMIT landing after the deadline fired takes the stock off the shelf
// AND creates the container that holds it, because both are the same commit. What
// the worker loses is only the RAM side — addContainer never ran, so the loot sits
// invisible in the DB until a restart's LoadAll picks it up (the same residue
// logInstallError describes for a deployed object). Logged at ERROR with the
// station, ship and player so it can be reconciled by hand rather than waited out.
func (w *Worker) logRobError(err error, s *sectorState, ship *domain.Ship, target domain.EntityRef) {
	if dbDeadline(err) {
		w.logger.Error("hack outcome in doubt: loot container may exist in the DB while missing from RAM until restart",
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

package sector

import (
	"context"
	"math"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// EntityKilledTopic is the bus topic the kill handler publishes to. Future
// consumers (AI 5.x, bounties 6.3, stats 7.1) subscribe here; nothing
// listens yet, so publishing is best-effort.
const EntityKilledTopic = "entity.killed"

// EntityKilledEvent is the payload broadcast when an object is destroyed:
// who died (Victim), where (SectorID + Pos), and — for a ship victim (6.3) —
// who killed it (Killer, the attributed player) and whose ship it was
// (VictimPlayer, carried because the ship row is gone by the time a consumer
// reacts). Killer/VictimPlayer are 0 for static kills and unattributed deaths.
type EntityKilledEvent struct {
	Victim       domain.EntityRef
	SectorID     domain.SectorID
	Pos          domain.Vec2
	Killer       domain.PlayerID
	VictimPlayer domain.PlayerID
	// VictimIsSpacesuit reports whether the dead ship was a player's spacesuit
	// (phase 10.1) — the respawn handler spawns a fresh ship at home instead of
	// another suit. False for static kills and non-suit ships.
	VictimIsSpacesuit bool
	// VictimPassengers are the players riding the dead ship as passengers
	// (phase 10.23). The respawn handler ejects each into a spacesuit at the
	// death spot. Nil for ships without passengers and for NPC victims with no
	// player riders.
	VictimPassengers []domain.PlayerID
}

// missileGoodsType is the cargo goods-type the SP treats specially in the
// kill drop (probabilistic throw, see kill_object.md §3). Matches the
// "Missile" seed in migration 0017.
const missileGoodsType domain.GoodsTypeID = 50

// slavesGoodsType is the contraband a killed passenger ship spills — matches
// the "Slaves" seed in migration 0027. Phase 5.6.
const slavesGoodsType domain.GoodsTypeID = 323

// containerDropRadius is the ring radius the kill handler spreads
// multiple drop containers onto so a multi-stack wreck does not stack
// every container on one pixel (SP jitters ±20; we use a deterministic
// ring like nudgeDroneSpawn so tests stay reproducible).
const containerDropRadius = 15.0

// sweepKilledShips removes every ship whose HP reached 0 this tick. It runs
// after all combat phases (lasers, towers, missiles, drones), so a ship that
// crossed to HP=0 from any source is handled exactly once — the four damage
// sites already skip dead targets. Port of the ship branch of SP KillObject;
// see kill_object.md §2.
func (w *Worker) sweepKilledShips(ctx context.Context, s *sectorState) {
	var dead []domain.ShipID
	for id, ship := range s.ships {
		// MaxHP>0 distinguishes a real ship destroyed in combat from a
		// degenerate fixture that simply never had a hull defined (HP=0,
		// MaxHP=0). Every spawned ship has MaxHP=StartHP>0.
		if ship.HP <= 0 && ship.MaxHP > 0 {
			dead = append(dead, id)
		}
	}
	for _, id := range dead {
		w.killShip(ctx, s, s.ships[id])
	}
}

// killShip destroys one ship: drops its cargo (and, for a passenger ship,
// a "Slaves" container) into containers (immediate, transactional) and
// removes it from RAM. With no container repo wired (pure unit tests) it is
// a RAM-only removal.
func (w *Worker) killShip(ctx context.Context, s *sectorState, ship *domain.Ship) {
	if w.containerRepo != nil {
		w.dropLoot(ctx, s, ship)
	}
	// Standing penalty (9.4): destroying a police-race navy ship drops the
	// killer's standing with that race. LastAttacker is the attributed player
	// (same one bounties use); the scanner ignores NPC killers.
	if w.police != nil && w.policeRaces[ship.Race] && ship.LastAttacker != 0 {
		if err := w.police.OnRaceShipKilled(ctx, ship.LastAttacker, ship.Race); err != nil {
			w.logger.ErrorContext(ctx, "police: standing on race-ship kill",
				"err", err, "killer", int64(ship.LastAttacker), "race", int(ship.Race))
		}
	}
	// War reputation (10.3.13): the attributed killer's war_rate grows when they
	// destroy a ship. LastAttacker is the same player bounties/standing use; the
	// awarder skips NPC/zero killers. Immediate write inside the tick, like the
	// police standing drop above.
	if w.reputation != nil && ship.LastAttacker != 0 {
		if err := w.reputation.OnShipKilled(ctx, ship.LastAttacker); err != nil {
			w.logger.ErrorContext(ctx, "reputation: war on kill",
				"err", err, "killer", int64(ship.LastAttacker), "victim", int64(ship.ID))
		}
	}
	delete(s.ships, ship.ID)
	delete(s.dirty, ship.ID)
	delete(s.policeScanCooldown, ship.ID)
	w.publishEntityKilled(ctx, s, ship)
}

// publishEntityKilled emits the entity_killed bus event for downstream
// consumers (AI, bounties, stats), attributing the kill to the ship's last
// attacker (6.3). Best-effort: a nil bus (no WithHandoff) or a publish error
// is logged but never blocks the kill.
func (w *Worker) publishEntityKilled(ctx context.Context, s *sectorState, ship *domain.Ship) {
	w.publishKilled(ctx, s, EntityKilledEvent{
		Victim:            domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)},
		SectorID:          s.sectorID,
		Pos:               ship.Pos,
		Killer:            ship.LastAttacker,
		VictimPlayer:      ship.PlayerID,
		VictimIsSpacesuit: ship.IsSpacesuit,
		VictimPassengers:  clonePlayerIDs(ship.PassengerPlayers),
	})
}

// dropLoot reads the dead ship's cargo, plans the drop (SP mechanics via
// combat.PlanShipDrops), and asks the repo to delete the ship and spawn
// the drop containers atomically. The returned containers are added to
// the live set so the next snapshot broadcasts them. On a read error the
// plan falls back to empty so RecordKill still deletes the ship.
func (w *Worker) dropLoot(ctx context.Context, s *sectorState, ship *domain.Ship) {
	items, err := w.containerRepo.ShipCargo(ctx, ship.ID)
	if err != nil {
		w.logger.ErrorContext(ctx, "kill: load ship cargo",
			"err", err, "ship", int64(ship.ID), "sector", int64(s.sectorID))
		items = nil
	}

	plan := combat.PlanShipDrops(items, missileGoodsType, w.rng)
	// Passenger ships (5.5) spill a fraction of their passengers as a Slaves
	// container (5.6 / SP drop_slaves_on_kill). It rides the same drop list,
	// so it gets a ring slot and one container like any other stack.
	if ship.Passengers > 0 {
		if count, ok := combat.PlanSlavesDrop(ship.Passengers, w.rng); ok {
			plan = append(plan, combat.Drop{GoodsType: slavesGoodsType, Quantity: count})
		}
	}
	expiresAt := w.clock.Now().Add(w.cfg.ContainerTTL)
	drops := make([]domain.ContainerDrop, len(plan))
	for i, d := range plan {
		drops[i] = domain.ContainerDrop{
			Pos:       containerDropPos(ship.Pos, i, len(plan)),
			ExpiresAt: expiresAt,
			GoodsType: d.GoodsType,
			Quantity:  d.Quantity,
		}
	}

	containers, err := w.containerRepo.RecordKill(ctx, ship.ID, s.sectorID, drops)
	if err != nil {
		w.logger.ErrorContext(ctx, "kill: record kill",
			"err", err, "ship", int64(ship.ID), "sector", int64(s.sectorID))
		return
	}
	for i := range containers {
		s.addContainer(&containers[i])
	}
}

// containerDropPos spreads n drop containers onto a small ring around the
// wreck. A single drop sits on the wreck itself; n>1 fan out evenly.
func containerDropPos(center domain.Vec2, i, n int) domain.Vec2 {
	if n <= 1 {
		return center
	}
	a := 2 * math.Pi * float64(i) / float64(n)
	return center.Add(domain.Vec2{X: containerDropRadius * math.Cos(a), Y: containerDropRadius * math.Sin(a)})
}

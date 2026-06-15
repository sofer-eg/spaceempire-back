package sector

import (
	"context"
	"errors"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
)

// tickPlayerMining runs the player-driven ore mining (phase 10.3.6) once per
// tick, after tickAI so it never races the NPC miner's applyMine path. For
// every ship with an armed MiningTarget it gates on up_drill, holds the ship on
// station, checks range + energy, drills cfg.MineRate ore from the asteroid,
// debits the action energy, and deposits the ore by up_drill level:
//
//   - level 1   -> a loot container in space next to the asteroid;
//   - level >= 2 -> straight into the ship's hold (container fallback on a full
//     hold) — the same direct deposit the NPC miner uses.
//
// A depleted asteroid clears the ship's MiningTarget; a missing up_drill,
// out-of-range drift, or insufficient energy stops this tick's drilling without
// clearing the mode (it resumes once the gate passes again).
func (w *Worker) tickPlayerMining(ctx context.Context, s *sectorState) {
	for _, ship := range s.ships {
		if ship.MiningTarget == nil {
			continue
		}
		w.mineForPlayer(ctx, s, ship)
	}
}

// mineForPlayer is one ship's drill tick. It returns nothing — every outcome
// (drill, gate-fail, depletion) is reflected directly in the sector state.
func (w *Worker) mineForPlayer(ctx context.Context, s *sectorState, ship *domain.Ship) {
	// A ship can lose its up_drill mid-session (outfit change). Without the
	// module the mode is dead — clear it so the ship is free to fly again.
	level := shipEquipmentLevel(ship, "up_drill")
	if level < 1 {
		ship.MiningTarget = nil
		s.markDirty(ship.ID)
		return
	}

	ast, ok := s.asteroids[*ship.MiningTarget]
	if !ok {
		// Asteroid gone (depleted by another tick). Stop mining.
		ship.MiningTarget = nil
		s.markDirty(ship.ID)
		return
	}

	// Hold station: a drilling ship parks next to the asteroid (same stance as
	// the NPC applyMine). Zero the velocity too so applyMovement does not coast
	// it off station after a leftover Target.
	if ship.Target != nil || ship.FinalTarget != nil || ship.AttackTarget != nil || !ship.Vel.IsZero() {
		ship.Target = nil
		ship.FinalTarget = nil
		ship.AttackTarget = nil
		ship.Vel = domain.Vec2{}
		s.markDirty(ship.ID)
	}

	// Out of range: the ship drifted away (e.g. pushed by collision). Keep the
	// mode armed but skip this tick — it resumes once back in range.
	if ship.Pos.Sub(ast.Pos).Length() > w.cfg.MineRange {
		return
	}

	// Energy gate (phase 10.3.1): drilling is an "action" expense. Below the
	// cost this tick does not drill; the mode stays armed waiting for recharge.
	if ship.Energy < w.cfg.MineEnergyCost {
		return
	}

	amount := w.cfg.MineRate
	if amount > ast.Mass {
		amount = ast.Mass
	}
	if amount <= 0 {
		w.depleteAsteroid(ctx, s, ast)
		ship.MiningTarget = nil
		s.markDirty(ship.ID)
		return
	}

	// Deposit before debiting energy / mass: if the deposit fails (e.g. DB
	// error), the asteroid and energy are untouched and the tick retries.
	if !w.depositMinedOre(ctx, s, ship, ast, level, amount) {
		return
	}

	if w.cfg.MineEnergyCost > 0 {
		ship.Energy -= w.cfg.MineEnergyCost
	}
	ast.Mass -= amount
	s.markDirty(ship.ID)
	s.markAsteroidDirty(ast.ID)
	if ast.Mass <= 0 {
		w.depleteAsteroid(ctx, s, ast)
		ship.MiningTarget = nil
	}
}

// depositMinedOre routes amount units of the asteroid's ore to its destination
// by up_drill level and reports whether the deposit succeeded. Level 1 always
// drops a container; level >= 2 deposits into the hold and falls back to a
// container when the hold is full. A failed/no-op container drop (no repo wired
// in unit tests) still reports success so the asteroid mines down — the ore is
// simply lost, matching the applyMine "no minerLogistics" degradation.
func (w *Worker) depositMinedOre(ctx context.Context, s *sectorState, ship *domain.Ship, ast *domain.Asteroid, level int, amount int64) bool {
	if level >= 2 && w.minerLogistics != nil {
		shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)}
		err := w.minerLogistics.AddOre(ctx, shipRef, ast.OreType, amount)
		if err == nil {
			return true
		}
		if !errors.Is(err, cargo.ErrNoSpace) {
			// A real deposit error (DB down): leave the asteroid intact and
			// retry next tick, like applyMine.
			w.logger.ErrorContext(ctx, "player mining add ore failed",
				"err", err, "ship", int64(ship.ID), "asteroid", int64(ast.ID),
				"ore", int(ast.OreType), "amount", amount)
			return false
		}
		// Hold full: fall through to the container drop so the ore is not lost.
	}
	w.spawnOreContainer(ctx, s, ast, amount)
	return true
}

// spawnOreContainer drops amount units of the asteroid's ore as a loot
// container next to it (level-1 deposit, or the level-2 full-hold fallback).
// Reuses the kill-loot container machinery (SpawnContainer); a nil repo (unit
// tests) is a no-op — the ore is lost but the asteroid still mines down.
func (w *Worker) spawnOreContainer(ctx context.Context, s *sectorState, ast *domain.Asteroid, amount int64) {
	if w.containerRepo == nil {
		return
	}
	c, err := w.containerRepo.SpawnContainer(ctx, s.sectorID, domain.ContainerDrop{
		Pos:       containerDropPos(ast.Pos, 1, 2), // off-centre so it is not on the asteroid
		ExpiresAt: w.clock.Now().Add(w.cfg.ContainerTTL),
		GoodsType: ast.OreType,
		Quantity:  amount,
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "player mining spawn ore container failed",
			"err", err, "asteroid", int64(ast.ID), "ore", int(ast.OreType), "amount", amount)
		return
	}
	s.addContainer(&c)
}

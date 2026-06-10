package sector

import (
	"context"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// sectorWorldView is the read-only ai.WorldView the worker hands each
// controller during tickAI. It wraps the live sectorState plus a pointer to
// the controlled ship; every accessor returns copies so a controller cannot
// mutate sector state directly (it influences the world only by returning
// an ai.Action).
type sectorWorldView struct {
	s    *sectorState
	self *domain.Ship
}

// Self returns a value copy of the controlled ship. The copy is shallow:
// its pointer fields (Target, FinalTarget, …) alias the live ship, so a
// controller must treat the result as read-only and express intent through
// the returned Action rather than by writing through those pointers.
func (v sectorWorldView) Self() domain.Ship { return *v.self }

// Ships returns deep value copies of every ship in the sector, sorted by
// id (reuses the snapshot copier, so pointer fields are independent).
func (v sectorWorldView) Ships() []domain.Ship { return snapshotShips(v.s.ships) }

// Statics returns a copy of the sector's static objects.
func (v sectorWorldView) Statics() domain.SectorStatics { return cloneStatics(v.s.statics) }

// Asteroids returns value copies of every live asteroid in the sector,
// sorted by id. Miners read it to detect arrival/depletion and to re-pick a
// target when their current asteroid is mined out.
func (v sectorWorldView) Asteroids() []domain.Asteroid { return v.s.snapshotAsteroids() }

// tickAI runs every AI controller in the sector once, applying the action
// each returns. Controllers whose ship has left the sector (death, future
// handoff) are pruned. Runs inside the worker's single tick goroutine, so
// applying actions preserves the one-writer-per-sector invariant.
func (w *Worker) tickAI(ctx context.Context, s *sectorState) {
	if len(s.controllers) == 0 {
		return
	}
	for shipID, ctrl := range s.controllers {
		ship, ok := s.ships[shipID]
		if !ok {
			delete(s.controllers, shipID)
			continue
		}
		action := ctrl.Tick(ctx, sectorWorldView{s: s, self: ship})
		w.applyAIAction(ctx, s, ship, action)
	}
}

// applyAIAction is the worker-side dispatch of an ai.Action onto the live
// ship. It is the single extension point for new NPC capabilities: later
// phases add cases here next to the action types in package ai. Each action
// expresses the ship's full intent for the tick. Unknown/nil actions are
// no-ops. Runs inside the worker's tick goroutine, so a Transfer's DB
// transaction blocks the tick exactly like production.Tick does.
func (w *Worker) applyAIAction(ctx context.Context, s *sectorState, ship *domain.Ship, action ai.Action) {
	switch a := action.(type) {
	case ai.MoveTo:
		// "Go here, don't fight" — set the waypoint and drop any engagement.
		target := a.Target
		ship.Target = &target
		ship.AttackTarget = nil
		s.markDirty(ship.ID)
	case ai.Attack:
		// Engage: fire at the target (4.2 fireLasers reads AttackTarget) and
		// steer toward its current position to close to range. A target that
		// is not a live ship in this sector still arms AttackTarget but
		// leaves the waypoint unchanged.
		ref := a.Target
		ship.AttackTarget = &ref
		if ref.Kind == domain.EntityKindShip {
			if target, ok := s.ships[domain.ShipID(ref.ID)]; ok {
				pos := target.Pos
				ship.Target = &pos
			}
		}
		s.markDirty(ship.ID)
	case ai.SetCourse:
		// Arm the autopilot for a (possibly cross-sector) destination. The
		// existing resolveAutopilot/tryAutoJump steps take over from here.
		course := a.Course
		ship.FinalTarget = &course
		ship.Target = nil
		ship.CurrentTargetRef = cloneEntityRef(course.Approach)
		ship.AttackTarget = nil
		s.markDirty(ship.ID)
	case ai.Transfer:
		// Move goods between two cargo owners in a DB transaction, in-tick.
		// Nil logistics (unit tests without DB) makes it a logged no-op.
		if w.traderLogistics == nil {
			return
		}
		if err := w.traderLogistics.Haul(ctx, a.From, a.To, a.GoodsType, a.MaxUnits); err != nil {
			w.logger.ErrorContext(ctx, "trader haul failed",
				"err", err, "ship", int64(ship.ID),
				"from_kind", a.From.Kind, "from_id", a.From.ID,
				"to_kind", a.To.Kind, "to_id", a.To.ID,
				"goods", int(a.GoodsType))
		}
	case ai.Mine:
		// Drill the asteroid: subtract from its mass and deposit the ore into
		// the ship's hold, holding the ship in place while it works.
		w.applyMine(ctx, s, ship, a)
	case ai.BoardPassengers:
		// Board a random batch of passengers as the ship leaves a station.
		ship.Passengers = rollPassengers(w.rng, a.Max)
		s.markDirty(ship.ID)
		w.immediateSave(ship)
	case ai.DropPassengers:
		// Passengers disembark on arrival.
		if ship.Passengers != 0 {
			ship.Passengers = 0
			s.markDirty(ship.ID)
			w.immediateSave(ship)
		}
	case ai.Idle, nil:
		// nothing to do this tick
	}
}

// rollPassengers returns a passenger count in [1, max] using the worker's
// RNG (combat.RNG exposes only Float64). max<=1 always yields 1.
func rollPassengers(rng RNG, max int) int {
	if max <= 1 {
		return 1
	}
	n := 1 + int(rng.Float64()*float64(max))
	if n > max {
		n = max // guard the Float64()==~1.0 edge
	}
	return n
}

// applyMine executes an ai.Mine action: it looks up the target asteroid in
// the sector, holds the ship on station (clears Target/FinalTarget), drills
// up to a.Amount (capped by the asteroid's remaining mass), deposits that
// ore into the ship's hold, and decrements the asteroid — deleting it when
// mined out. A missing asteroid (already depleted) or a nil minerLogistics
// (unit tests without a DB) degrades to just the hold/cleanup half. Runs in
// the worker's tick goroutine, so the AddOre transaction blocks the tick
// exactly like a Transfer does.
func (w *Worker) applyMine(ctx context.Context, s *sectorState, ship *domain.Ship, a ai.Mine) {
	// Hold position: a drilling ship parks next to the asteroid. Zero the
	// velocity too — clearing Target alone would let applyMovement coast the
	// ship off station.
	if ship.Target != nil || ship.FinalTarget != nil || ship.AttackTarget != nil || !ship.Vel.IsZero() {
		ship.Target = nil
		ship.FinalTarget = nil
		ship.AttackTarget = nil
		ship.Vel = domain.Vec2{}
		s.markDirty(ship.ID)
	}

	ast, ok := s.asteroids[a.Asteroid]
	if !ok {
		return // already mined out by a prior tick
	}
	amount := a.Amount
	if amount > ast.Mass {
		amount = ast.Mass
	}
	if amount <= 0 {
		w.depleteAsteroid(ctx, s, ast)
		return
	}

	if w.minerLogistics != nil {
		shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)}
		if err := w.minerLogistics.AddOre(ctx, shipRef, ast.OreType, amount); err != nil {
			// Hold full (or another deposit error): leave the asteroid intact
			// and try again next tick. The miner's own load counter still
			// advances it home, so this does not spin forever.
			w.logger.ErrorContext(ctx, "miner add ore failed",
				"err", err, "ship", int64(ship.ID), "asteroid", int64(ast.ID),
				"ore", int(ast.OreType), "amount", amount)
			return
		}
	}

	ast.Mass -= amount
	s.markAsteroidDirty(ast.ID)
	if ast.Mass <= 0 {
		w.depleteAsteroid(ctx, s, ast)
	}
}

// depleteAsteroid removes a mined-out asteroid from RAM and persists the
// deletion immediately (so it never reappears after a restart). A nil repo
// (unit tests) drops it from RAM only.
func (w *Worker) depleteAsteroid(ctx context.Context, s *sectorState, ast *domain.Asteroid) {
	id := ast.ID
	s.removeAsteroid(id)
	if w.asteroidRepo == nil {
		return
	}
	if err := w.asteroidRepo.Delete(ctx, id); err != nil {
		w.logger.ErrorContext(ctx, "delete depleted asteroid failed",
			"err", err, "sector", int64(s.sectorID), "asteroid", int64(id))
	}
}

// buildControllers hydrates the sector's AI controllers from its cold-start
// ai_state rows. Called once per owned sector from NewWorker (always, even
// with no AI wired) so s.controllers is non-nil and tickAI is safe. A row
// whose controller_kind is not registered (stale config) is logged and
// skipped — it must not abort the world load.
func (w *Worker) buildControllers(s *sectorState, states []domain.AIState) {
	s.controllers = make(map[domain.ShipID]ai.Controller, len(states))
	if w.aiRegistry == nil {
		return
	}
	for _, st := range states {
		ctrl, err := w.aiRegistry.Build(st.ControllerKind, st.StateJSON)
		if err != nil {
			w.logger.Error("build ai controller",
				"err", err, "ship", int64(st.ShipID),
				"kind", st.ControllerKind, "sector", int64(s.sectorID))
			continue
		}
		s.controllers[st.ShipID] = ctrl
	}
}

// persistAIState is the periodic-snapshot step for AI controller state. It
// piggybacks on the same SnapshotInterval cadence as ships/drones: at most
// once per interval it upserts every live controller's marshalled state.
// Controller state changes nearly every tick (route phase advances), so
// there is no dirty-tracking — all controllers are written each cycle.
func (w *Worker) persistAIState(ctx context.Context, s *sectorState) {
	if w.aiStateRepo == nil || len(s.controllers) == 0 {
		return
	}
	if w.clock.Now().Sub(s.lastAISnapshot) < w.cfg.SnapshotInterval {
		return
	}
	states := w.collectAIState(s)
	if len(states) > 0 {
		if err := w.aiStateRepo.BatchUpsert(ctx, states); err != nil {
			w.logger.ErrorContext(ctx, "ai state snapshot failed",
				"err", err, "sector", int64(s.sectorID), "count", len(states))
			return
		}
	}
	s.lastAISnapshot = w.clock.Now()
}

// collectAIState marshals every live controller into an AIState row. A
// controller that fails to marshal is logged and skipped (its row keeps the
// previous snapshot's value rather than aborting the whole batch).
func (w *Worker) collectAIState(s *sectorState) []domain.AIState {
	out := make([]domain.AIState, 0, len(s.controllers))
	for shipID, ctrl := range s.controllers {
		data, err := ctrl.MarshalState()
		if err != nil {
			w.logger.Error("marshal ai state",
				"err", err, "ship", int64(shipID), "sector", int64(s.sectorID))
			continue
		}
		out = append(out, domain.AIState{
			ShipID:         shipID,
			SectorID:       s.sectorID,
			ControllerKind: ctrl.Kind(),
			StateJSON:      data,
		})
	}
	return out
}

// flushAIState writes every controller's current state on graceful
// shutdown, so a clean restart resumes the latest phase even when it falls
// between periodic snapshots (mirrors flushDrones). No-op when AI-state
// persistence is disabled or the sector has no controllers.
func (w *Worker) flushAIState(ctx context.Context, s *sectorState) {
	if w.aiStateRepo == nil || len(s.controllers) == 0 {
		return
	}
	states := w.collectAIState(s)
	if len(states) == 0 {
		return
	}
	if err := w.aiStateRepo.BatchUpsert(ctx, states); err != nil {
		w.logger.ErrorContext(ctx, "shutdown flush ai state failed",
			"err", err, "sector", int64(s.sectorID), "count", len(states))
	}
}

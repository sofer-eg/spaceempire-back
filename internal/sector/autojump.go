package sector

// tryAutoJump scans every ship in the sector and, for those on autopilot
// (FinalTarget set, current sector != target sector), fires a handoff if
// the ship is within GateRange of the gate exit pointing toward the next
// hop. Called from the worker's tick after applyMovement so any auto-jump
// uses the just-updated position.
//
// Without a configured router or bus/topology, the function is a no-op —
// the autopilot then simply parks the ship at the gate exit until the
// player issues an explicit JumpCommand.
func (w *Worker) tryAutoJump(s *sectorState) {
	if w.router == nil || w.topology == nil || w.bus == nil {
		return
	}
	for id, ship := range s.ships {
		if ship.Docked != nil {
			continue
		}
		if ship.FinalTarget == nil || ship.SectorID == ship.FinalTarget.Sector {
			continue
		}
		next, ok := w.router.NextSector(ship.SectorID, ship.FinalTarget.Sector)
		if !ok {
			continue
		}
		gate := w.router.GateBetween(ship.SectorID, next)
		if gate == nil {
			continue
		}
		sourcePos, targetSector, exitPos, ok := gateSides(gate, ship.SectorID)
		if !ok {
			continue
		}
		if ship.Pos.Sub(sourcePos).Length() > w.cfg.GateRange {
			continue
		}
		if err := executeJump(w, s, ship, targetSector, exitPos); err != nil {
			w.logger.Error("auto-jump failed",
				"err", err,
				"ship", int64(id),
				"from", int64(ship.SectorID),
				"to", int64(targetSector))
			continue
		}
	}
}

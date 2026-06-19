package sector

// tryAutoDock auto-completes docking for ships on an Approach-style course
// that carry a Docking Computer (up_docking, phase 10.3.10). When such a ship
// reaches DockRange of its approach target it docks without an explicit
// DockCommand — the Go port of the original StarWind up_docking gate (SP
// Docking, db.sql:4413). Ships without the module still park at DockRange/2
// (resolveApproach) and wait for the player's manual /api/cmd/dock.
//
// Called from the worker's tick after applyMovement so the dock decision uses
// the just-updated position. Mirrors tryAutoJump (its in-sector counterpart):
// a ship is either approaching a gate in another sector (auto-jump) or a
// static in its own sector (auto-dock), never both.
func (w *Worker) tryAutoDock(s *sectorState) {
	for id, ship := range s.ships {
		if ship.Docked != nil {
			continue
		}
		if ship.FinalTarget == nil || ship.FinalTarget.Approach == nil {
			continue
		}
		if ship.SectorID != ship.FinalTarget.Sector {
			continue
		}
		if shipEquipmentLevel(ship, "up_docking") < 1 {
			continue
		}
		target, err := lookupStatic(s.statics, *ship.FinalTarget.Approach)
		if err != nil {
			continue
		}
		if ship.Pos.Sub(target.ObjectPos()).Length() > w.cfg.DockRange {
			continue
		}
		if err := executeDock(w, s, ship, target, w.cfg.DockRange); err != nil {
			w.logger.Error("auto-dock failed",
				"err", err,
				"ship", int64(id),
				"target", *ship.FinalTarget.Approach)
			continue
		}
	}
}

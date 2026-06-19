package sector

import "spaceempire/back/internal/domain"

// ExternalDockCommand starts the up_exdocking external-docking process (phase
// 10.3.23): a multi-tick clamp-on to a moving host ship that the ship's hangar
// cannot hold. Gated on an installed up_exdocking module (which the catalog
// only fits on class 7, so the original "class 7 may re-initiate" rule is
// satisfied automatically — re-issuing simply restarts the counter). Unlike a
// normal hangar dock (DockCommand on a ship target) the completion bypasses the
// hangar-capacity check, attaching to the host's hull instead.
type ExternalDockCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	Reply    chan<- CmdResult
}

func (c ExternalDockCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked != nil:
		res.Err = ErrAlreadyDocked
	case shipEquipmentLevel(ship, "up_exdocking") < 1:
		res.Err = ErrEquipmentRequired
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}
	host, err := lookupShip(s, c.Target)
	if err != nil {
		res.Err = err
		replyOnce(c.Reply, res)
		return
	}
	if err := w.externalDockGates(ship, host); err != nil {
		res.Err = err
		replyOnce(c.Reply, res)
		return
	}
	ship.ExternalDock = &domain.ExternalDock{Target: c.Target, TurnsLeft: w.cfg.ExternalDockTurns}
	replyOnce(c.Reply, res)
}

// externalDockGates is the initiate/completion validation shared by
// ExternalDockCommand and the per-tick completion: self, sector, range and
// hostility. It deliberately omits the open/owner and hangar-capacity gates —
// external docking exists precisely to attach when those would block.
func (w *Worker) externalDockGates(ship, host *domain.Ship) error {
	if host.ID == ship.ID {
		return ErrDockSelf
	}
	if host.SectorID != ship.SectorID {
		return ErrTargetSectorMismatch
	}
	if ship.Pos.Sub(host.Pos).Length() > w.cfg.DockRange {
		return ErrDockOutOfRange
	}
	if w.relations != nil &&
		w.relations.IsHostile(domain.PlayerRef(ship.PlayerID), domain.PlayerRef(host.PlayerID)) {
		return ErrDockHostile
	}
	return nil
}

// executeExternalAttach completes the external dock: re-validate the gates (the
// host may have moved out of range during the process) and, on success, park
// the ship on the host via applyShipDock — the same ride-along attach as a
// hangar dock, but without the hangar-capacity check.
func (w *Worker) executeExternalAttach(s *sectorState, ship, host *domain.Ship) error {
	if err := w.externalDockGates(ship, host); err != nil {
		return err
	}
	return w.applyShipDock(s, ship, host)
}

// tickExternalDock advances every in-progress external-docking process one tick
// (phase 10.3.23). The counter is decremented by replacing the pointer (never
// mutating TurnsLeft in place) so a published snapshot aliasing it never races.
// On the last tick the ship attaches (bypassing hangar capacity) or, if the
// host vanished / drifted out of range, the process is silently cancelled.
func (w *Worker) tickExternalDock(s *sectorState) {
	for _, ship := range s.ships {
		if ship.ExternalDock == nil {
			continue
		}
		if ship.Docked != nil {
			ship.ExternalDock = nil
			continue
		}
		if ship.ExternalDock.TurnsLeft > 1 {
			ship.ExternalDock = &domain.ExternalDock{
				Target:    ship.ExternalDock.Target,
				TurnsLeft: ship.ExternalDock.TurnsLeft - 1,
			}
			continue
		}
		w.completeExternalDock(s, ship)
	}
}

// completeExternalDock fires the final tick of an external dock: clear the
// process state and attach to the host, logging (and dropping) a cancellation.
func (w *Worker) completeExternalDock(s *sectorState, ship *domain.Ship) {
	target := ship.ExternalDock.Target
	ship.ExternalDock = nil
	host, err := lookupShip(s, target)
	if err != nil {
		w.logger.Debug("external dock cancelled: host gone",
			"ship", int64(ship.ID), "target", target)
		return
	}
	if err := w.executeExternalAttach(s, ship, host); err != nil {
		w.logger.Debug("external dock cancelled",
			"ship", int64(ship.ID), "host", int64(host.ID), "err", err)
	}
}

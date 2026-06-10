package sector

import (
	"context"
	"fmt"

	"spaceempire/back/internal/domain"
)

// SetShipAccessCommand toggles whether OTHER players may board the ship as a
// passenger (phase 10.23). Own ships are always boardable by their owner; this
// flag only gates boarding by other players. Ownership is enforced. Persisted
// immediately (a deliberate player choice, like attack target).
type SetShipAccessCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Open     bool
	Reply    chan<- CmdResult
}

func (c SetShipAccessCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}
	prev := ship.IsOpen
	ship.IsOpen = c.Open
	if w.repo != nil {
		if err := w.repo.Save(context.Background(), *ship); err != nil {
			ship.IsOpen = prev
			res.Err = fmt.Errorf("save ship access: %w", err)
			replyOnce(c.Reply, res)
			return
		}
	}
	s.markDirty(c.ShipID)
	replyOnce(c.Reply, res)
}

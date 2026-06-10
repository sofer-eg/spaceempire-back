package sector

import (
	"context"

	"spaceempire/back/internal/domain"
)

// PickupContainerCommand transfers a loot container's cargo into the
// player's ship and removes the container. Ownership is enforced and the
// ship must be within PickupRange of the container. The cargo move +
// container delete is one transaction in the container repo (Pickup); the
// worker only validates and mutates RAM after a successful write.
type PickupContainerCommand struct {
	PlayerID    domain.PlayerID
	ShipID      domain.ShipID
	ContainerID domain.ContainerID
	Reply       chan<- CmdResult
}

func (c PickupContainerCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	default:
		res.Err = w.pickupContainer(s, ship, c.ContainerID)
	}
	replyOnce(c.Reply, res)
}

// pickupContainer validates proximity, asks the repo to move the cargo
// and delete the container (atomic, all-or-nothing — a full ship returns
// the repo's no-space error), then drops it from RAM. A pickup error from
// the repo (no space, missing row) leaves the container in place.
func (w *Worker) pickupContainer(s *sectorState, ship *domain.Ship, id domain.ContainerID) error {
	container, ok := s.containers[id]
	if !ok {
		return ErrContainerNotFound
	}
	if !pointInRadius2(ship.Pos, container.Pos, w.cfg.PickupRange*w.cfg.PickupRange) {
		return ErrContainerOutOfRange
	}
	if w.containerRepo != nil {
		if err := w.containerRepo.Pickup(context.Background(), id, ship.ID); err != nil {
			return err
		}
	}
	s.removeContainer(id)
	return nil
}

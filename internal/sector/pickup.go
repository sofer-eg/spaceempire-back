package sector

import (
	"context"
	"errors"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/containers"
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
		err := w.dbCall(context.Background(), func(ctx context.Context) error {
			return w.containerRepo.Pickup(ctx, id, ship.ID)
		})
		if errors.Is(err, containers.ErrContainerNotFound) {
			// The row is gone but RAM still shows the container: a ghost left by an
			// earlier pickup whose COMMIT landed after its deadline fired (see
			// logPickupError). Sweep it now instead of leaving it on the player's
			// radar for the rest of its TTL, and answer with the sector-level
			// sentinel so the HTTP layer maps it to 404 rather than 500 — the
			// persistence sentinel is a different error value and falls through to
			// "internal error", which is not what "this is not there" should read as.
			s.removeContainer(id)
			w.logger.Warn("pickup found no container row: sweeping the stale RAM entry",
				"container", int64(id), "ship", int64(ship.ID),
				"player", int64(ship.PlayerID), "sector", int64(s.sectorID))
			return ErrContainerNotFound
		}
		if err != nil {
			w.logPickupError(err, s, ship, id)
			return err
		}
	}
	s.removeContainer(id)
	return nil
}

// logPickupError records a failed pickup (TASK-148). The order above — commit,
// then drop the container from RAM — is what keeps an ambiguous deadline from
// costing the player cargo, and it is worth spelling out because it is the
// AC that this whole change turns on:
//
//   - the DB, not RAM, is the cargo ledger. Repository.Pickup moves the cargo,
//     deletes the container's cargo rows and deletes the container in ONE
//     transaction, so the goods are either in the hold or in the container —
//     never in neither, and never in both;
//   - a deadline that fires while that COMMIT is in flight therefore leaves the
//     cargo safely in the hold, with the only residue a container still sitting
//     in the sector's RAM. That ghost is picked up again at worst once: the retry
//     re-enters the same transaction, finds no container row and returns
//     ErrContainerNotFound, so it cannot duplicate the goods — and that refusal
//     is also what sweeps the ghost out of RAM on the spot (see the not-found
//     branch above), so it does not linger on the radar for its whole TTL;
//   - the opposite order (drop from RAM first, then commit) would make the same
//     deadline delete the container from the sector while the transaction rolled
//     back — cargo that existed a moment ago, gone from the game with its row
//     intact, visible again only after a restart. That is the silent loss AC#3
//     forbids.
//
// So the deadline is ERROR — the outcome is genuinely unknown and the ghost is
// worth a look — and every other error is WARN: the transaction rolled back, the
// container is untouched, and the player already got the reason back on the ack.
func (w *Worker) logPickupError(err error, s *sectorState, ship *domain.Ship, id domain.ContainerID) {
	if dbDeadline(err) {
		w.logger.Error("pickup outcome in doubt: cargo may be in the hold while the container is still in the sector",
			"err", err, "container", int64(id), "ship", int64(ship.ID),
			"player", int64(ship.PlayerID), "sector", int64(s.sectorID),
			"repo_timeout", w.cfg.RepoTimeout)
		return
	}
	w.logger.Warn("pickup failed",
		"err", err, "container", int64(id), "ship", int64(ship.ID),
		"player", int64(ship.PlayerID), "sector", int64(s.sectorID))
}

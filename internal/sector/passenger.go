package sector

import "spaceempire/back/internal/domain"

// AddPassengerCommand registers a player as a passenger of the host ship (phase
// 10.23). The host's PassengerPlayers is the RAM mirror used to fan out
// PlayerHandoffEvent on a gate jump (B6) and to eject riders on death (B8);
// the persistent source of truth is players.passenger_of_ship_id, written by
// the board handler. Idempotent: a player already aboard is not duplicated.
type AddPassengerCommand struct {
	HostID   domain.ShipID
	PlayerID domain.PlayerID
	Reply    chan<- CmdResult
}

func (c AddPassengerCommand) apply(_ *Worker, s *sectorState) {
	host, ok := s.ships[c.HostID]
	if !ok {
		replyOnce(c.Reply, CmdResult{Err: ErrShipNotFound})
		return
	}
	for _, p := range host.PassengerPlayers {
		if p == c.PlayerID {
			replyOnce(c.Reply, CmdResult{})
			return
		}
	}
	host.PassengerPlayers = append(host.PassengerPlayers, c.PlayerID)
	s.markDirty(c.HostID)
	replyOnce(c.Reply, CmdResult{})
}

// RemovePassengerCommand drops a passenger from the host's RAM mirror (phase
// 10.23: disembark, or re-home after the host died). Idempotent.
type RemovePassengerCommand struct {
	HostID   domain.ShipID
	PlayerID domain.PlayerID
	Reply    chan<- CmdResult
}

func (c RemovePassengerCommand) apply(_ *Worker, s *sectorState) {
	host, ok := s.ships[c.HostID]
	if !ok {
		replyOnce(c.Reply, CmdResult{})
		return
	}
	host.PassengerPlayers = removePlayer(host.PassengerPlayers, c.PlayerID)
	s.markDirty(c.HostID)
	replyOnce(c.Reply, CmdResult{})
}

func removePlayer(in []domain.PlayerID, p domain.PlayerID) []domain.PlayerID {
	out := in[:0:0]
	for _, x := range in {
		if x != p {
			out = append(out, x)
		}
	}
	return out
}

// clonePlayerIDs deep-copies a passenger slice so the in-RAM ship and the
// snapshot / jump-event copy do not alias the same backing array.
func clonePlayerIDs(in []domain.PlayerID) []domain.PlayerID {
	if len(in) == 0 {
		return nil
	}
	return append([]domain.PlayerID(nil), in...)
}

package app

import (
	"context"

	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
)

// warRatePerKill is the war_rate a player gains for each ship they destroy
// (phase 10.3.13). StarWind accrues users.warstatus through a damage-weighted
// combat-experience formula; the MVP grants a flat point per kill — enough to
// move players off zero so the rank gate (10.3.4) min_war_rate becomes reachable.
const warRatePerKill = 1

// reputationAdder is the slice of players.Repository the awarder needs (ISP).
// *players.Repository satisfies it.
type reputationAdder interface {
	AddReputation(ctx context.Context, playerID domain.PlayerID, delta playersrepo.Reputation) (playersrepo.Reputation, error)
}

// reputationAwarder implements sector.ReputationAwarder over players.AddReputation
// (phase 10.3.13): it grants war_rate to a real player credited with a kill.
// NPC/zero killers are ignored, mirroring policeScanner.OnRaceShipKilled.
type reputationAwarder struct {
	players reputationAdder
	npc     domain.PlayerID
}

// OnShipKilled grants the killer warRatePerKill war reputation. NPC and zero
// killers are skipped.
func (a reputationAwarder) OnShipKilled(ctx context.Context, killer domain.PlayerID) error {
	if killer == 0 || killer == a.npc {
		return nil
	}
	_, err := a.players.AddReputation(ctx, killer, playersrepo.Reputation{War: warRatePerKill})
	return err
}

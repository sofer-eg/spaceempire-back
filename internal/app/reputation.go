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

// captureRacePenalty is the standing drop a player takes with the race of a ship
// they capture (TASK-100.3.9.4), mirroring the police KillPenalty for destroying a
// navy ship. Flat like warRatePerKill (the capture roll thresholds are the tunable
// knobs in capture.yaml; the reputation magnitude stays a code constant).
const captureRacePenalty = 10

// reputationAdder is the slice of players.Repository the awarder needs (ISP).
// *players.Repository satisfies it.
type reputationAdder interface {
	AddReputation(ctx context.Context, playerID domain.PlayerID, delta playersrepo.Reputation) (playersrepo.Reputation, error)
}

// reputationAwarder implements sector.ReputationAwarder over players.AddReputation
// (phase 10.3.13): it grants war_rate to a real player credited with a kill or a
// capture, and drops their race standing on a capture. NPC/zero actors are ignored,
// mirroring policeScanner.OnRaceShipKilled.
type reputationAwarder struct {
	players  reputationAdder
	standing standingAdjuster
	npc      domain.PlayerID
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

// OnShipCaptured grants the capturer war_rate and, for a main-race (1-5) victim,
// drops their standing with that race (TASK-100.3.9.4, SP DoCapture success). NPC
// and zero capturers are skipped; race 0 (player ship) and the pirate/xenon/kha'ak
// races carry no per-player standing, so only the war grant applies to them.
func (a reputationAwarder) OnShipCaptured(ctx context.Context, capturer domain.PlayerID, race domain.RaceID) error {
	if capturer == 0 || capturer == a.npc {
		return nil
	}
	if _, err := a.players.AddReputation(ctx, capturer, playersrepo.Reputation{War: warRatePerKill}); err != nil {
		return err
	}
	if isMainRace(race) {
		if _, err := a.standing.Adjust(ctx, capturer, race, -captureRacePenalty); err != nil {
			return err
		}
	}
	return nil
}

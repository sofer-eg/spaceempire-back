package sector

import (
	"context"

	"spaceempire/back/internal/domain"
)

// ReputationAwarder grants combat reputation (war_rate) to the player credited
// with a kill (phase 10.3.13). The worker calls OnShipKilled from killShip with
// the dead ship's LastAttacker; the app-side implementation skips the NPC owner
// and applies the delta via players.AddReputation. Wired via WithReputation; nil
// disables war-rate accrual (pure unit tests need no wiring) — the same split as
// PoliceScanner keeps the sector package free of the players dependency.
type ReputationAwarder interface {
	OnShipKilled(ctx context.Context, killer domain.PlayerID) error
	// OnShipCaptured grants the capturer war_rate and drops their standing with the
	// captured ship's race (TASK-100.3.9.4, SP DoCapture success). race is the
	// captured ship's race read BEFORE changeShipOwner neutralises it; race 0
	// (player ship) and the pirate/xenon/kha'ak races carry no per-player standing,
	// so only the war grant applies. NPC/zero capturers are skipped by the impl.
	OnShipCaptured(ctx context.Context, capturer domain.PlayerID, race domain.RaceID) error
}

// WithReputation wires combat-reputation accrual (phase 10.3.13): when a player
// destroys a ship, the attributed killer's war_rate grows. Nil disables it.
func WithReputation(a ReputationAwarder) Option {
	return func(w *Worker) {
		w.reputation = a
	}
}

package pacer

import (
	"context"

	"spaceempire/back/internal/domain"
)

// OnJump advances the player's jump counter and, when the jump threshold is
// reached, offers one "intercepted signal" quest (source="space"). playerSector
// is the sector the player arrived in — the generator's late-binding location.
func (p *Pacer) OnJump(ctx context.Context, player domain.PlayerID, playerSector domain.SectorID) error {
	return p.trigger(ctx, player, playerSector, triggerJump)
}

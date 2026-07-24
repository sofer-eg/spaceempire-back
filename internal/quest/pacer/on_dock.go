package pacer

import (
	"context"

	"spaceempire/back/internal/domain"
)

// OnDock advances the player's dock counter and, when the dock threshold is
// reached, offers one "bulletin board" quest (source="dock"). playerSector is
// the ship's sector at dock time — the generator's late-binding location.
func (p *Pacer) OnDock(ctx context.Context, player domain.PlayerID, playerSector domain.SectorID) error {
	return p.trigger(ctx, player, playerSector, triggerDock)
}

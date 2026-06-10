package app

import (
	"spaceempire/back/internal/domain"
	raceref "spaceempire/back/internal/reference/race"
)

// raceMatrixTargeter resolves Race-AI hostility from the ship races via the
// default standing matrix (phase 9.1, ported in 8.13). A race ship attacks
// another ship when race.DefaultStanding(self.Race, other.Race) < 0 — so a
// pirate (6) raids the main races and traders, Xenon (7) / Kha'ak (8) attack
// everyone, a factionless player (race 0) is a valid target for the
// hostile-by-default races but ignored by the main-race navy (1–5). The
// per-player wanted overlay for the main races lands in 9.4.
//
// This replaces the relations-based targeter for the race controller: NPC
// faction ships share one __npc__ owner, so player/clan relations cannot tell
// a pirate from an Argon navy ship — only the race can. Player↔player combat
// gating still runs through relations (sector.WithHostility / WithRelations).
type raceMatrixTargeter struct{}

func (raceMatrixTargeter) IsHostile(self, other domain.Ship) bool {
	return raceref.IsHostile(self.Race, other.Race)
}

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/domain"
)

// TestUnit_RaceMatrixTargeter pins the 9.1 wiring: the race controller's
// hostility comes from ship.Race via the 8.13 matrix, so NPC factions fight
// each other despite sharing one __npc__ owner.
func TestUnit_RaceMatrixTargeter(t *testing.T) {
	tg := raceMatrixTargeter{}
	ship := func(race domain.RaceID) domain.Ship { return domain.Ship{Race: race} }

	// Hostile pairs (DefaultStanding < 0).
	assert.True(t, tg.IsHostile(ship(1), ship(7)), "Argon navy attacks Xenon")
	assert.True(t, tg.IsHostile(ship(6), ship(1)), "pirate raids Argon")
	assert.True(t, tg.IsHostile(ship(7), ship(1)), "Xenon attacks everyone")
	assert.True(t, tg.IsHostile(ship(8), ship(7)), "Kha'ak hates Xenon (asymmetry)")
	assert.True(t, tg.IsHostile(ship(6), ship(0)), "pirate attacks a factionless player")
	assert.True(t, tg.IsHostile(ship(7), ship(0)), "Xenon attacks a factionless player")

	// Friendly / neutral pairs.
	assert.False(t, tg.IsHostile(ship(1), ship(1)), "navy spares its own race")
	assert.False(t, tg.IsHostile(ship(1), ship(2)), "Argon and Boron are allied")
	assert.False(t, tg.IsHostile(ship(7), ship(8)), "Xenon likes Kha'ak")
	assert.False(t, tg.IsHostile(ship(1), ship(0)), "main-race navy ignores a neutral player")
}

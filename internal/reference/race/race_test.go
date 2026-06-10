package race_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/reference/race"
)

func TestUnit_Race_Lookup(t *testing.T) {
	// Spot-check values against the old `races` table + js/map.js palette.
	cases := []struct {
		id        domain.RaceID
		name      string
		stateName string
		color     string
		sector    domain.SectorID
		station   int64
	}{
		{0, "Нейтральный", "", "#ffffff", 0, 0},
		{1, "Аргон", "Аргонская Федерация", "#ec7a7c", 1, 18},
		{2, "Борон", "Королевство Борон", "#647ab4", 5, 34},
		{5, "Телади", "Корпорация", "#7cc6a4", 17, 82},
		{6, "Пират", "Пираты", "#6c6e6c", 21, 98},
		{7, "Ксенон", "Ксенон", "#ff3030", 25, 114},
		{8, "Хаак", "Гегемония Хааков", "#ff5a5a", 215, 130},
	}
	for _, c := range cases {
		d, ok := race.Lookup(c.id)
		require.Truef(t, ok, "race %d must exist", c.id)
		assert.Equal(t, c.name, d.Name, "race %d name", c.id)
		assert.Equal(t, c.stateName, d.StateName, "race %d state", c.id)
		assert.Equal(t, c.color, d.Color, "race %d color", c.id)
		assert.Equal(t, c.sector, d.CentralSector, "race %d central sector", c.id)
		assert.Equal(t, c.station, d.CentralStation, "race %d central station", c.id)
	}

	_, ok := race.Lookup(999)
	assert.False(t, ok, "unknown race id must not resolve")
}

func TestUnit_Race_All(t *testing.T) {
	all := race.All()
	// 0–8 + service races 98/100.
	require.Len(t, all, 11)

	seen := make(map[domain.RaceID]bool, len(all))
	for _, d := range all {
		require.Falsef(t, seen[d.ID], "duplicate race id %d", d.ID)
		seen[d.ID] = true
		assert.NotEmptyf(t, d.Color, "race %d must have a colour", d.ID)
	}
	assert.True(t, seen[0] && seen[1] && seen[8] && seen[98] && seen[100])
}

func TestUnit_Race_DefaultStanding(t *testing.T) {
	// Diagonal: same race is an ally.
	for id := domain.RaceID(1); id <= 8; id++ {
		assert.Equalf(t, race.StandingAlly, race.DefaultStanding(id, id), "race %d self-standing", id)
	}

	// Main-race rows reproduce race_relations.Standing.
	assert.Equal(t, 10, race.DefaultStanding(1, 2)) // Argon → Boron
	assert.Equal(t, 1, race.DefaultStanding(1, 3))  // Argon → Paranid
	assert.Equal(t, 5, race.DefaultStanding(1, 5))  // Argon → Teladi
	assert.Equal(t, -5, race.DefaultStanding(1, 6)) // Argon → Pirate
	assert.Equal(t, -10, race.DefaultStanding(1, 7))
	assert.Equal(t, 10, race.DefaultStanding(3, 5)) // Paranid → Teladi

	// The Xenon/Kha'ak asymmetry must NOT be symmetrised.
	assert.Equal(t, 10, race.DefaultStanding(7, 8), "Xenon likes Kha'ak")
	assert.Equal(t, -10, race.DefaultStanding(8, 7), "Kha'ak hates Xenon")

	// Pirate has no matrix rows → hostile-by-default toward non-pirate.
	assert.Equal(t, -5, race.DefaultStanding(6, 1))
	assert.Equal(t, race.StandingAlly, race.DefaultStanding(6, 6))

	// Toward the neutral pseudo-race (factionless player).
	assert.Equal(t, -5, race.DefaultStanding(6, race.Neutral))
	assert.Equal(t, -10, race.DefaultStanding(7, race.Neutral))
	assert.Equal(t, -10, race.DefaultStanding(8, race.Neutral))
	assert.Equal(t, race.StandingNeutral, race.DefaultStanding(1, race.Neutral))
}

// TestUnit_Race_IsHostile pins the phase-8.3 parity: pirate/xenon/kha'ak
// objects attack a factionless player (race 0); main races (1–5) stay passive.
func TestUnit_Race_IsHostile(t *testing.T) {
	for _, from := range []domain.RaceID{6, 7, 8} {
		assert.Truef(t, race.IsHostile(from, race.Neutral), "race %d must be hostile to players", from)
	}
	for from := domain.RaceID(1); from <= 5; from++ {
		assert.Falsef(t, race.IsHostile(from, race.Neutral), "main race %d must stay passive to players", from)
	}

	// Sign agrees with DefaultStanding.
	assert.True(t, race.IsHostile(1, 7))  // Argon vs Xenon: war
	assert.False(t, race.IsHostile(7, 8)) // Xenon likes Kha'ak
	assert.True(t, race.IsHostile(8, 7))  // Kha'ak hates Xenon
	assert.False(t, race.IsHostile(1, 2)) // allies
	assert.False(t, race.IsHostile(6, 6)) // pirate vs pirate
}

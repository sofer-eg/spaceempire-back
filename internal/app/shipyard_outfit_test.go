package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/sector"
)

// fakeStandingReader is an in-memory (player, race) -> standing map.
type fakeStandingReader map[[2]int64]int

func (f fakeStandingReader) Get(player domain.PlayerID, race domain.RaceID) int {
	return f[[2]int64{int64(player), int64(race)}]
}

// raceGatedCatalog has one module (id 1) that needs race standing >= 5.
func raceGatedCatalog(t *testing.T) *balance.Equipments {
	t.Helper()
	eq, err := balance.NewEquipments([]balance.Equipment{
		{ID: 1, Type: "up_lb", MaxLevel: 1, Race: 0, ShipClass: 5, Price: 100, Dependance: "none", MinRaceRate: 5},
	})
	require.NoError(t, err)
	return eq
}

// TestUnit_OutfitServer_GatedInstall_UsesShipyardRaceStanding proves the install
// rank gate (phase 10.3.14) sources the race axis from the player's standing
// with the shipyard's race — not an aggregate. The same player passes at a
// shipyard of a race they stand well with and fails at one they do not.
func TestUnit_OutfitServer_GatedInstall_UsesShipyardRaceStanding(t *testing.T) {
	t.Parallel()
	// Player 100 stands +5 with race 2 (meets the bar) and +4 with race 3 (below).
	standing := fakeStandingReader{
		{100, 2}: 5,
		{100, 3}: 4,
	}
	s := &outfitServer{equipment: raceGatedCatalog(t), standing: standing}

	// Shipyard race 2: standing 5 >= min_race_rate 5 → installs.
	row, err := s.gatedInstall(1, 5, 0, 1, nil, 100, playersrepo.Reputation{}, 2)
	require.NoError(t, err)
	assert.Equal(t, "up_lb", row.Type)

	// Shipyard race 3: standing 4 < 5 → rank too low (handler maps this to 422).
	_, err = s.gatedInstall(1, 5, 0, 1, nil, 100, playersrepo.Reputation{}, 3)
	require.ErrorIs(t, err, balance.ErrRankTooLow)
}

// TestUnit_OutfitServer_GatedInstall_WarTradeFromPlayerRecord proves war/trade
// still come from the player's single ratings (unchanged), independent of the
// race axis.
func TestUnit_OutfitServer_GatedInstall_WarTradeFromPlayerRecord(t *testing.T) {
	t.Parallel()
	eq, err := balance.NewEquipments([]balance.Equipment{
		{ID: 2, Type: "up_lb", MaxLevel: 1, Race: 0, ShipClass: 5, Price: 100, Dependance: "none", MinWarRate: 100},
	})
	require.NoError(t, err)
	s := &outfitServer{equipment: eq, standing: fakeStandingReader{}}

	_, err = s.gatedInstall(2, 5, 0, 1, nil, 100, playersrepo.Reputation{War: 99}, 0)
	require.ErrorIs(t, err, balance.ErrRankTooLow)

	row, err := s.gatedInstall(2, 5, 0, 1, nil, 100, playersrepo.Reputation{War: 100}, 0)
	require.NoError(t, err)
	assert.Equal(t, "up_lb", row.Type)
}

func TestUnit_FindShipyardRace(t *testing.T) {
	t.Parallel()
	snap := sector.Snapshot{Statics: domain.SectorStatics{Shipyards: []domain.Shipyard{
		{ID: 7, Race: 3},
		{ID: 9, Race: 1},
	}}}
	assert.Equal(t, 3, findShipyardRace(snap, 7))
	assert.Equal(t, 1, findShipyardRace(snap, 9))
	assert.Equal(t, 0, findShipyardRace(snap, 99), "unknown shipyard → neutral")
}

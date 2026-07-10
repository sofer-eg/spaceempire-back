package balance_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// AC-10 (TASK-100.3.9.3): with up_hack's racial gate removed in the real
// configs/equipment.yaml (race 6 → 0), a factionless (race 0) player installs
// the module — provided they meet the preserved race-standing gate
// (min_race_rate 2) and the up_accumulator dependency.
func TestUnit_UpHack_RaceGateRemoved_InstallsForNeutralPlayer(t *testing.T) {
	t.Parallel()
	c, err := balance.LoadEquipmentFromFile("../../configs/equipment.yaml")
	require.NoError(t, err)

	e, ok := c.GetEquipment(122)
	require.True(t, ok, "up_hack (id 122) is in the catalog")
	require.Equal(t, "up_hack", e.Type)
	assert.Equal(t, 0, e.Race, "AC-10: racial gate removed (was 6)")
	assert.Equal(t, 2, e.MinRaceRate, "reputation gate preserved")

	installed := []domain.InstalledEquipment{{Type: "up_accumulator", Level: 1}}

	// A race-0 player with race-standing >= 2 installs up_hack — no
	// ErrEquipmentWrongRace.
	got, err := c.ResolveInstall(122, 0, 0, 1, installed, balance.Reputation{Race: 2})
	require.NoError(t, err)
	assert.Equal(t, "up_hack", got.Type)

	// Below the race-standing threshold it is still gated (min_race_rate kept).
	_, err = c.ResolveInstall(122, 0, 0, 1, installed, balance.Reputation{Race: 1})
	assert.ErrorIs(t, err, balance.ErrRankTooLow)
}

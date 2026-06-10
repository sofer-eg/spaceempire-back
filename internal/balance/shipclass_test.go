package balance_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

func TestUnit_NewShipClasses_HappyPath(t *testing.T) {
	c, err := balance.NewShipClasses([]balance.ShipClass{
		{ID: 73, Race: 1, Type: 1, Class: 1, Name: "Колосс", PilotCabin: 60},
		{ID: 81, Race: 1, Type: 9, Class: 9, Name: "Меркурий", CargoBay: 2000},
	})
	require.NoError(t, err)
	require.Equal(t, 2, c.ShipClassCount())

	colossus, ok := c.GetShipClass(73)
	require.True(t, ok)
	assert.Equal(t, "Колосс", colossus.Name)
	assert.Equal(t, balance.CategoryCarrier, colossus.Category())
	assert.Equal(t, "Носитель", balance.CategoryLabel(colossus.Category()))

	mercury, _ := c.GetShipClass(81)
	assert.Equal(t, balance.CategoryTransport, mercury.Category())
}

func TestUnit_NewShipClasses_RejectsBadInput(t *testing.T) {
	_, err := balance.NewShipClasses([]balance.ShipClass{{ID: 0, Name: "A"}})
	require.ErrorIs(t, err, balance.ErrInvalidShipClassID)

	_, err = balance.NewShipClasses([]balance.ShipClass{{ID: 1, Name: ""}})
	require.ErrorIs(t, err, balance.ErrEmptyShipClassName)

	_, err = balance.NewShipClasses([]balance.ShipClass{
		{ID: 1, Name: "A"}, {ID: 1, Name: "B"},
	})
	require.ErrorIs(t, err, balance.ErrDuplicateShipClassID)
}

func TestUnit_ShipClassCategory_MapsClassColumn(t *testing.T) {
	cases := map[int]balance.ShipClassCategory{
		1: balance.CategoryCarrier,
		2: balance.CategoryDestroyer,
		3: balance.CategoryHeavyFighter,
		4: balance.CategoryFighter,
		5: balance.CategoryScout,
		6: balance.CategoryCorvette,
		7: balance.CategoryFreighter,
		8: balance.CategorySpecial,
		9: balance.CategoryTransport,
	}
	for class, want := range cases {
		got := balance.ShipClass{Class: class}.Category()
		assert.Equalf(t, want, got, "class %d", class)
	}
	// Unknown class number degrades to special, not a panic.
	assert.Equal(t, balance.CategorySpecial, balance.ShipClass{Class: 99}.Category())
}

// TestUnit_LoadShipClasses_RealCatalog loads the converted catalog and checks
// parity with the original ct_ship_classes dump (86 rows, named ships).
func TestUnit_LoadShipClasses_RealCatalog(t *testing.T) {
	c, err := balance.LoadShipClassesFromFile(filepath.Join("..", "..", "configs", "ship_classes.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 86, c.ShipClassCount(), "ct_ship_classes had 86 rows")

	// Spot-check named ships across races and categories.
	colossus, ok := c.GetShipClass(73)
	require.True(t, ok)
	assert.Equal(t, "Колосс", colossus.Name)
	assert.Equal(t, 1, colossus.Race)
	assert.Equal(t, balance.CategoryCarrier, colossus.Category())
	assert.Equal(t, 60, colossus.PilotCabin)

	xenonJ, ok := c.GetShipClass(127)
	require.True(t, ok)
	assert.Equal(t, "Ксенон Ж", xenonJ.Name)
	assert.Equal(t, 7, xenonJ.Race)

	khaakCarrier, ok := c.GetShipClass(136)
	require.True(t, ok)
	assert.Equal(t, "Носитель Хааков", khaakCarrier.Name)
	assert.Equal(t, 8, khaakCarrier.Race)

	// Argon should have the full nine standard classes (type 1..9).
	argon := c.ShipClassesByRace(1)
	assert.Len(t, argon, 9)

	// Mercury is the Argon TS transport.
	mercury, ok := c.GetShipClass(81)
	require.True(t, ok)
	assert.Equal(t, "Меркурий", mercury.Name)
	assert.Equal(t, balance.CategoryTransport, mercury.Category())
	_ = domain.ShipClassID(0) // typed-id sanity
}

func TestUnit_ScoutForRace(t *testing.T) {
	c, err := balance.NewShipClasses([]balance.ShipClass{
		{ID: 1, Race: 1, Class: 1, Name: "Колосс"},    // M1
		{ID: 2, Race: 1, Class: 5, Name: "Разведчик"}, // M5 (Argon scout)
		{ID: 3, Race: 2, Class: 5, Name: "Осьминог"},  // M5 (Boron scout)
	})
	require.NoError(t, err)

	argon, ok := c.ScoutForRace(1)
	require.True(t, ok)
	assert.Equal(t, "Разведчик", argon.Name)
	assert.Equal(t, balance.CategoryScout, argon.Category())

	boron, ok := c.ScoutForRace(2)
	require.True(t, ok)
	assert.Equal(t, "Осьминог", boron.Name)

	_, ok = c.ScoutForRace(9) // race with no scout in the catalog
	assert.False(t, ok)
}

package balance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
)

// LoadShipClassesFromFile defaults the radar radius per gameplay category when
// the YAML omits it (ct_ship_classes has no radar column), and honours an
// explicit `radar:` override (phase 10.20 L1).
func TestUnit_LoadShipClasses_RadarDefaultByCategory(t *testing.T) {
	yaml := `ship_classes:
    - id: 1
      race: 1
      type: 5
      class: 5
      name: Разведчик
      hull: 4000
    - id: 2
      race: 1
      type: 1
      class: 1
      name: Колосс
      hull: 200000
    - id: 3
      race: 1
      type: 4
      class: 4
      name: Сделанный-на-заказ
      hull: 12000
      radar: 9999
`
	path := filepath.Join(t.TempDir(), "ship_classes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

	cat, err := balance.LoadShipClassesFromFile(path)
	require.NoError(t, err)

	scout, ok := cat.GetShipClass(1)
	require.True(t, ok)
	require.Equal(t, balance.CategoryScout, scout.Category())
	require.Equal(t, 3500, scout.Radar, "M5 scout gets the scout default")

	carrier, ok := cat.GetShipClass(2)
	require.True(t, ok)
	require.Equal(t, balance.CategoryCarrier, carrier.Category())
	require.Equal(t, 2400, carrier.Radar, "M1 carrier gets the capital default")

	custom, ok := cat.GetShipClass(3)
	require.True(t, ok)
	require.Equal(t, 9999, custom.Radar, "explicit YAML radar overrides the default")
}

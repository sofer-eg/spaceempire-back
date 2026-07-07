package balance

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"spaceempire/back/internal/domain"
)

// yamlLoadoutModule / yamlLoadout mirror the on-disk shape produced by
// cmd/starwind-tools/convert-ship-loadout.
type yamlLoadoutModule struct {
	EquipmentID int    `yaml:"equipment_id"`
	Type        string `yaml:"type"`
	Level       int    `yaml:"level"`
}

type yamlLoadout struct {
	Race    int                 `yaml:"race"`
	Type    int                 `yaml:"type"`
	Modules []yamlLoadoutModule `yaml:"modules"`
}

type yamlLoadoutFile struct {
	Loadouts []yamlLoadout `yaml:"loadouts"`
}

// LoadShipLoadoutsFromFile reads, parses and validates the base-loadout catalog
// YAML at path. The returned *ShipLoadouts is ready to inject (TASK-100.3.25).
func LoadShipLoadoutsFromFile(path string) (*ShipLoadouts, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ship loadouts: read %s: %w", path, err)
	}

	var f yamlLoadoutFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("ship loadouts: parse %s: %w", path, err)
	}

	loadouts := make([]ShipLoadout, 0, len(f.Loadouts))
	for _, l := range f.Loadouts {
		mods := make([]domain.InstalledEquipment, 0, len(l.Modules))
		for _, m := range l.Modules {
			mods = append(mods, domain.InstalledEquipment{
				EquipmentID: domain.EquipmentID(m.EquipmentID),
				Type:        m.Type,
				Level:       m.Level,
			})
		}
		if len(mods) == 0 {
			mods = nil
		}
		loadouts = append(loadouts, ShipLoadout{Race: l.Race, Type: l.Type, Modules: mods})
	}

	return NewShipLoadouts(loadouts)
}

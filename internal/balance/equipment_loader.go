package balance

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"spaceempire/back/internal/domain"
)

// yamlEquipment mirrors the ct_updates columns produced by
// cmd/starwind-tools/convert-equipment (is_base kept as int 0/1, like the dump).
type yamlEquipment struct {
	ID            int    `yaml:"id"`
	Type          string `yaml:"type"`
	Description   string `yaml:"description"`
	MaxLevel      int    `yaml:"max_level"`
	Race          int    `yaml:"race"`
	Class         int    `yaml:"class"`
	Price         int64  `yaml:"price"`
	PricePerLevel int64  `yaml:"price_per_level"`
	MinWarRate    int    `yaml:"min_war_rate"`
	MinTradeRate  int    `yaml:"min_trade_rate"`
	MinRaceRate   int    `yaml:"min_race_rate"`
	IsBase        int    `yaml:"is_base"`
	Position      int    `yaml:"position"`
	Dependance    string `yaml:"dependance"`
	EnergyUseType string `yaml:"energy_use_type"`
	EnergyUsage   int    `yaml:"energy_usage"`
}

type yamlEquipmentFile struct {
	Equipment []yamlEquipment `yaml:"equipment"`
}

// LoadEquipmentFromFile reads, parses and validates the equipment catalog
// YAML at path. The returned *Equipments is ready to inject. Phase 8.16.
func LoadEquipmentFromFile(path string) (*Equipments, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("balance: read %s: %w", path, err)
	}

	var f yamlEquipmentFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("balance: parse %s: %w", path, err)
	}

	items := make([]Equipment, 0, len(f.Equipment))
	for _, e := range f.Equipment {
		items = append(items, Equipment{
			ID:            domain.EquipmentID(e.ID),
			Type:          e.Type,
			Description:   e.Description,
			MaxLevel:      e.MaxLevel,
			Race:          e.Race,
			ShipClass:     e.Class,
			Price:         e.Price,
			PricePerLevel: e.PricePerLevel,
			MinWarRate:    e.MinWarRate,
			MinTradeRate:  e.MinTradeRate,
			MinRaceRate:   e.MinRaceRate,
			IsBase:        e.IsBase != 0,
			Position:      e.Position,
			Dependance:    e.Dependance,
			EnergyUseType: e.EnergyUseType,
			EnergyUsage:   e.EnergyUsage,
		})
	}

	return NewEquipments(items)
}

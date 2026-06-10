package balance

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"spaceempire/back/internal/domain"
)

// yamlShipClass mirrors the on-disk shape produced by
// cmd/starwind-tools/convert-ship-classes.
type yamlShipClass struct {
	ID              int     `yaml:"id"`
	Race            int     `yaml:"race"`
	Type            int     `yaml:"type"`
	Class           int     `yaml:"class"`
	Name            string  `yaml:"name"`
	Speed           float64 `yaml:"speed"`
	Acceleration    float64 `yaml:"acceleration"`
	Laser           int     `yaml:"laser"`
	Shield          int     `yaml:"shield"`
	Hull            int     `yaml:"hull"`
	ShieldCharge    int     `yaml:"shield_charge"`
	Maneuverability float64 `yaml:"maneuverability"`
	CargoBay        int     `yaml:"cargobay"`
	BasePrice       int64   `yaml:"base_price"`
	HangerSmall     int     `yaml:"hanger_small"`
	HangerCapital   int     `yaml:"hanger_capital"`
	HangerShipType  int     `yaml:"hanger_ship_type"`
	HangerShipSpace int     `yaml:"hanger_ship_space"`
	PilotCabin      int     `yaml:"pilot_cabin"`
	JumpFuel        float64 `yaml:"jump_fuel"`
	// Radar is optional (phase 10.20): ct_ship_classes has no radar column, so
	// the converter does not emit it and rows omit it — radarForCategory then
	// supplies a per-category default. An explicit `radar:` overrides.
	Radar int `yaml:"radar"`
}

type yamlShipClassFile struct {
	ShipClasses []yamlShipClass `yaml:"ship_classes"`
}

// LoadShipClassesFromFile reads, parses and validates the ship-class YAML at
// path. The returned *ShipClasses is ready to inject.
func LoadShipClassesFromFile(path string) (*ShipClasses, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ship classes: read %s: %w", path, err)
	}

	var f yamlShipClassFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("ship classes: parse %s: %w", path, err)
	}

	classes := make([]ShipClass, 0, len(f.ShipClasses))
	for _, c := range f.ShipClasses {
		sc := ShipClass{
			ID:              domain.ShipClassID(c.ID),
			Race:            c.Race,
			Type:            c.Type,
			Class:           c.Class,
			Name:            c.Name,
			Speed:           c.Speed,
			Acceleration:    c.Acceleration,
			Laser:           c.Laser,
			Shield:          c.Shield,
			Hull:            c.Hull,
			ShieldCharge:    c.ShieldCharge,
			Maneuverability: c.Maneuverability,
			CargoBay:        c.CargoBay,
			BasePrice:       c.BasePrice,
			Radar:           c.Radar,
			HangerSmall:     c.HangerSmall,
			HangerCapital:   c.HangerCapital,
			HangerShipType:  c.HangerShipType,
			HangerShipSpace: c.HangerShipSpace,
			PilotCabin:      c.PilotCabin,
			JumpFuel:        c.JumpFuel,
		}
		// ct_ship_classes carries no radar — default it per category (phase
		// 10.20) unless the YAML overrode it.
		if sc.Radar <= 0 {
			sc.Radar = radarForCategory(sc.Category())
		}
		classes = append(classes, sc)
	}
	return NewShipClasses(classes)
}

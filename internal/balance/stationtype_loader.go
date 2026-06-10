package balance

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlStationType mirrors the station_types catalog rows produced by
// cmd/starwind-tools/convert-station-types.
type yamlStationType struct {
	ID       int    `yaml:"id"`
	Name     string `yaml:"name"`
	RaceID   int    `yaml:"race_id"`
	Kind     int    `yaml:"kind"`
	Hull     int    `yaml:"hull"`
	Shield   int    `yaml:"shield"`
	Sellable int    `yaml:"sellable"`
}

// yamlStationTypeFile is the on-disk shape of configs/station_types.yaml:
// the station catalog plus the production recipes (which moved here from
// balance.yaml, whose generator convert-balance only writes goods_types).
type yamlStationTypeFile struct {
	StationTypes []yamlStationType `yaml:"station_types"`
	Recipes      []yamlRecipe      `yaml:"recipes"`
}

// LoadStationTypesFromFile reads the station-type catalog and the production
// recipes from the YAML file at path. Recipes are returned separately so the
// caller can fold them into the *Balance (which validates them against the
// goods catalog) via balance.New. Phase 8.15.
func LoadStationTypesFromFile(path string) (*StationTypes, []Recipe, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("balance: read %s: %w", path, err)
	}

	var f yamlStationTypeFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, nil, fmt.Errorf("balance: parse %s: %w", path, err)
	}

	types := make([]StationType, 0, len(f.StationTypes))
	for _, t := range f.StationTypes {
		types = append(types, StationType{
			ID:       t.ID,
			Name:     t.Name,
			RaceID:   t.RaceID,
			Kind:     StationKind(t.Kind),
			Hull:     t.Hull,
			Shield:   t.Shield,
			Sellable: t.Sellable != 0,
		})
	}
	cat, err := NewStationTypes(types)
	if err != nil {
		return nil, nil, err
	}

	recipes := make([]Recipe, 0, len(f.Recipes))
	for _, r := range f.Recipes {
		dur, err := time.ParseDuration(r.CycleTime)
		if err != nil {
			return nil, nil, fmt.Errorf("balance: parse cycle_time for station_type=%d: %w", r.StationType, err)
		}
		recipes = append(recipes, Recipe{
			StationType: r.StationType,
			Inputs:      mapRecipeLines(r.Inputs),
			Outputs:     mapRecipeLines(r.Outputs),
			CycleTime:   dur,
		})
	}

	return cat, recipes, nil
}

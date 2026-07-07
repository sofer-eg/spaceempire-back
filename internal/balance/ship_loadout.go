package balance

import (
	"fmt"

	"spaceempire/back/internal/domain"
)

// ShipLoadout is the base equipment set a ship of a given race+type spawns with,
// ported from the StarWind ct_npc_ship_modules table (TASK-100.3.25). The
// original SP CreateStandartPilotShip fitted these modules directly, bypassing
// the dependance/rank gates the interactive shipyard enforces. Each module
// carries a resolved EquipmentID so the effect/energy folding
// (ApplyEquipmentEffects / EnergyDelta) can look the catalog row up by id.
type ShipLoadout struct {
	Race    int
	Type    int
	Modules []domain.InstalledEquipment
}

// ShipLoadouts is the immutable in-memory base-loadout catalog built from
// configs/ship_base_loadout.yaml. Build it once at startup and inject read-only.
// It is keyed by ship (race, type) — unique across the ship-class catalog — so a
// spawn/purchase can resolve the base kit from a ship class without the dump.
type ShipLoadouts struct {
	byRaceType map[[2]int][]domain.InstalledEquipment
	nonEmpty   int
}

// NewShipLoadouts validates the input and returns a *ShipLoadouts, or an error
// wrapping one of the package sentinels.
func NewShipLoadouts(loadouts []ShipLoadout) (*ShipLoadouts, error) {
	byRaceType := make(map[[2]int][]domain.InstalledEquipment, len(loadouts))
	nonEmpty := 0
	for _, l := range loadouts {
		key := [2]int{l.Race, l.Type}
		if _, dup := byRaceType[key]; dup {
			return nil, fmt.Errorf("%w: race=%d type=%d", ErrDuplicateShipLoadout, l.Race, l.Type)
		}
		for _, m := range l.Modules {
			if m.EquipmentID <= 0 {
				return nil, fmt.Errorf("%w: race=%d type=%d module=%s", ErrInvalidLoadoutEquipmentID, l.Race, l.Type, m.Type)
			}
			if m.Type == "" {
				return nil, fmt.Errorf("%w: race=%d type=%d id=%d", ErrEmptyLoadoutModuleType, l.Race, l.Type, m.EquipmentID)
			}
		}
		byRaceType[key] = l.Modules
		if len(l.Modules) > 0 {
			nonEmpty++
		}
	}
	return &ShipLoadouts{byRaceType: byRaceType, nonEmpty: nonEmpty}, nil
}

// BaseLoadout returns a defensive copy of the base module set for a ship of the
// given race+type, or nil when the race+type is unknown or spawns bare (the 14
// special models have no ct_npc_ship_modules rows upstream, faithful to the
// original). The copy lets callers hand the slice straight to a domain.Ship
// without aliasing the shared catalog.
func (c *ShipLoadouts) BaseLoadout(race, shipType int) []domain.InstalledEquipment {
	mods := c.byRaceType[[2]int{race, shipType}]
	if len(mods) == 0 {
		return nil
	}
	out := make([]domain.InstalledEquipment, len(mods))
	copy(out, mods)
	return out
}

// LoadoutCount is the number of race+type keys with a non-empty base loadout.
func (c *ShipLoadouts) LoadoutCount() int { return c.nonEmpty }

// TotalCount is the number of race+type keys loaded (empty ones included).
func (c *ShipLoadouts) TotalCount() int { return len(c.byRaceType) }

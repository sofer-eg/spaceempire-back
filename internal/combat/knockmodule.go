package combat

import "spaceempire/back/internal/domain"

// KnockConfig tunes the module-knockoff mechanic (port of SP DestroyModule,
// starwind/sql/db.sql:10012). The four scalars are a balance decision
// (configs/capture.yaml, ЧТЗ doc-4 §5.1); the faithful defaults are 0.2 / 0.7 /
// 0.2 / 0.1. Positions maps a module Type (up_engine, up_shield, …) to its slot
// (1 = inner, 2 = outer) so the roll can classify a ship's Equipment without a
// per-tick catalog lookup — the app builds it once from balance.Equipments.
type KnockConfig struct {
	// CriticalShieldCharge is the shield fraction at/below which modules start
	// falling off. Above it nothing is knocked (the main gate).
	CriticalShieldCharge float64
	// CriticalHullIntegrity is the hull fraction at/below which internal modules
	// (Position 1, incl. up_shield) become eligible. Above it internal chance is 0.
	CriticalHullIntegrity float64
	// ExternalBase scales the external-module (Position 2) knock chance, which
	// grows from 0 (shield at critical) to ExternalBase (shield at 0).
	ExternalBase float64
	// InternalBase scales the internal-module (Position 1) knock chance, which
	// grows from 0 (hull at critical) to InternalBase (hull at 0).
	InternalBase float64
	// Positions is the module-Type → slot (1 inner / 2 outer) lookup. A Type
	// absent from the map is treated as un-knockable (slot 0).
	Positions map[string]int
}

// DefaultKnockConfig returns the faithful DestroyModule scalars (ЧТЗ §5.1).
// Positions is left nil — the app fills it from the equipment catalog.
func DefaultKnockConfig() KnockConfig {
	return KnockConfig{
		CriticalShieldCharge:  0.2,
		CriticalHullIntegrity: 0.7,
		ExternalBase:          0.2,
		InternalBase:          0.1,
	}
}

// KnockModule rolls the DestroyModule mechanic for one weapon hit on a ship
// whose shield may be down, mutating ship.Equipment in place and returning the
// modules that fell off (0, 1, or 2 — at most one external and one internal).
//
// Gate/rolls (mirrors the SP, ЧТЗ §3 FR-A2..A4):
//   - shieldFrac > CriticalShieldCharge → nothing knocked (return nil).
//   - external (Position 2): chance (1 - shieldFrac/critical) * ExternalBase;
//     on a hit a random external module is removed.
//   - internal (Position 1, incl. up_shield): only when hullFrac <=
//     CriticalHullIntegrity, chance (1 - hullFrac/critical) * InternalBase; on a
//     hit a random internal module is removed. No priority for up_shield — it is
//     a lottery among the internal modules.
//
// The two rolls are independent, matching the SP. rng.Float64 is consumed once
// per chance roll and once per selection (in the order external-chance,
// external-select, internal-chance, internal-select). ship.Equipment is
// rebuilt as a fresh slice, so any snapshot aliasing the previous slice is left
// intact (NFR-003).
func KnockModule(ship *domain.Ship, shieldFrac, hullFrac float64, rng RNG, cfg KnockConfig) []domain.InstalledEquipment {
	if ship == nil || cfg.CriticalShieldCharge <= 0 || len(ship.Equipment) == 0 {
		return nil
	}
	if shieldFrac > cfg.CriticalShieldCharge {
		return nil
	}

	remove := make(map[int]struct{}, 2)

	extChance := (1 - shieldFrac/cfg.CriticalShieldCharge) * cfg.ExternalBase
	if rng.Float64() < extChance {
		if i, ok := pickByPosition(ship.Equipment, 2, cfg.Positions, rng); ok {
			remove[i] = struct{}{}
		}
	}

	if cfg.CriticalHullIntegrity > 0 && hullFrac <= cfg.CriticalHullIntegrity {
		intChance := (1 - hullFrac/cfg.CriticalHullIntegrity) * cfg.InternalBase
		if rng.Float64() < intChance {
			if j, ok := pickByPosition(ship.Equipment, 1, cfg.Positions, rng); ok {
				remove[j] = struct{}{}
			}
		}
	}

	if len(remove) == 0 {
		return nil
	}

	knocked := make([]domain.InstalledEquipment, 0, len(remove))
	remaining := make([]domain.InstalledEquipment, 0, len(ship.Equipment)-len(remove))
	for i, m := range ship.Equipment {
		if _, drop := remove[i]; drop {
			knocked = append(knocked, m)
		} else {
			remaining = append(remaining, m)
		}
	}
	if len(remaining) == 0 {
		remaining = nil
	}
	ship.Equipment = remaining
	return knocked
}

// pickByPosition returns the index of a random module whose slot matches pos
// (via the positions lookup), and true; false when the ship has no such module.
// One rng.Float64 is consumed only when at least one candidate exists.
func pickByPosition(eq []domain.InstalledEquipment, pos int, positions map[string]int, rng RNG) (int, bool) {
	var idxs []int
	for i, m := range eq {
		if positions[m.Type] == pos {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		return 0, false
	}
	k := int(rng.Float64() * float64(len(idxs)))
	if k >= len(idxs) {
		k = len(idxs) - 1 // guard the Float64()==~1.0 edge
	}
	return idxs[k], true
}

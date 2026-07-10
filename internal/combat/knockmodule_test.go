package combat_test

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// knockPositions is the Type→slot lookup the knock roll classifies with:
// up_launcher/up_hide are external (2), up_shield/up_engine internal (1).
var knockPositions = map[string]int{
	"up_launcher": 2,
	"up_hide":     2,
	"up_shield":   1,
	"up_engine":   1,
}

func knockCfg() combat.KnockConfig {
	c := combat.DefaultKnockConfig()
	c.Positions = knockPositions
	return c
}

func shipWith(mods ...domain.InstalledEquipment) *domain.Ship {
	return &domain.Ship{Equipment: mods}
}

// AC-1: shield above the critical charge → nothing knocked, no roll consumed.
func TestUnit_KnockModule_ShieldAboveCritical_NoKnock(t *testing.T) {
	t.Parallel()
	ship := shipWith(
		domain.InstalledEquipment{Type: "up_launcher", Level: 1},
		domain.InstalledEquipment{Type: "up_shield", Level: 1},
	)
	// queueRNG panics if rolled — proves the main gate returns before any roll.
	knocked := combat.KnockModule(ship, 0.5, 1.0, &queueRNG{}, knockCfg())
	assert.Empty(t, knocked)
	assert.Len(t, ship.Equipment, 2)
}

// AC-2: shield down → external (Position 2) module falls off; hull intact so no
// internal roll. The knocked module is gone from Equipment for good.
func TestUnit_KnockModule_ShieldDown_KnocksExternal(t *testing.T) {
	t.Parallel()
	ship := shipWith(
		domain.InstalledEquipment{Type: "up_launcher", Level: 1},
		domain.InstalledEquipment{Type: "up_shield", Level: 1},
	)
	// shieldFrac 0 → extChance 0.2; roll 0.1 hits; select 0.0 → up_launcher.
	// hullFrac 1.0 > 0.7 → internal chance 0, no internal roll.
	rng := &queueRNG{vals: []float64{0.1, 0.0}}
	knocked := combat.KnockModule(ship, 0.0, 1.0, rng, knockCfg())
	require.Len(t, knocked, 1)
	assert.Equal(t, "up_launcher", knocked[0].Type)
	assert.Equal(t, []domain.InstalledEquipment{{Type: "up_shield", Level: 1}}, ship.Equipment)
}

// External roll misses when the chance roll lands above the (shield-scaled)
// chance: nothing knocked, no selection roll consumed.
func TestUnit_KnockModule_ExternalRollMisses_NoKnock(t *testing.T) {
	t.Parallel()
	ship := shipWith(domain.InstalledEquipment{Type: "up_launcher", Level: 1})
	// shieldFrac 0.1 → extChance (1-0.5)*0.2 = 0.1; roll 0.5 > 0.1 → miss.
	rng := &queueRNG{vals: []float64{0.5}}
	knocked := combat.KnockModule(ship, 0.1, 1.0, rng, knockCfg())
	assert.Empty(t, knocked)
	assert.Len(t, ship.Equipment, 1)
}

// AC-3: shield down AND hull below the critical integrity → an internal
// (Position 1) module, up_shield among them, can be knocked off.
func TestUnit_KnockModule_HullDown_KnocksInternalShield(t *testing.T) {
	t.Parallel()
	ship := shipWith(
		domain.InstalledEquipment{Type: "up_launcher", Level: 1},
		domain.InstalledEquipment{Type: "up_shield", Level: 1},
	)
	// external chance 0.2 rolled 0.9 → miss (no select). hullFrac 0 → intChance
	// 0.1 rolled 0.05 → hit; select 0.0 → up_shield (only Position-1 module).
	rng := &queueRNG{vals: []float64{0.9, 0.05, 0.0}}
	knocked := combat.KnockModule(ship, 0.0, 0.0, rng, knockCfg())
	require.Len(t, knocked, 1)
	assert.Equal(t, "up_shield", knocked[0].Type)
	assert.Equal(t, []domain.InstalledEquipment{{Type: "up_launcher", Level: 1}}, ship.Equipment)
}

// Internal modules are safe while the hull stays above the critical integrity,
// even with the shield fully down.
func TestUnit_KnockModule_HullIntact_InternalSafe(t *testing.T) {
	t.Parallel()
	ship := shipWith(domain.InstalledEquipment{Type: "up_shield", Level: 1})
	// shield down → external chance rolled 0.9 (miss); hullFrac 0.8 > 0.7 → no
	// internal roll at all (chance 0). No Position-2 module, so external select
	// is never reached either.
	rng := &queueRNG{vals: []float64{0.9}}
	knocked := combat.KnockModule(ship, 0.0, 0.8, rng, knockCfg())
	assert.Empty(t, knocked)
	assert.Len(t, ship.Equipment, 1)
}

// One hit can knock both an external and an internal module (the two rolls are
// independent), stripping the ship bare.
func TestUnit_KnockModule_BothRollsHit_KnocksTwo(t *testing.T) {
	t.Parallel()
	ship := shipWith(
		domain.InstalledEquipment{Type: "up_launcher", Level: 1},
		domain.InstalledEquipment{Type: "up_shield", Level: 1},
	)
	// ext chance 0.1 hit, ext select 0.0 → up_launcher; int chance 0.05 hit,
	// int select 0.0 → up_shield.
	rng := &queueRNG{vals: []float64{0.1, 0.0, 0.05, 0.0}}
	knocked := combat.KnockModule(ship, 0.0, 0.0, rng, knockCfg())
	assert.Len(t, knocked, 2)
	assert.Nil(t, ship.Equipment)
}

// A module type absent from the Positions lookup is un-knockable (slot 0).
func TestUnit_KnockModule_UnknownPositionModuleSafe(t *testing.T) {
	t.Parallel()
	ship := shipWith(domain.InstalledEquipment{Type: "up_mystery", Level: 1})
	// shield down → external chance rolled 0.0 (hit), but no Position-2 module
	// so the select finds no candidate and nothing is removed.
	rng := &queueRNG{vals: []float64{0.0}}
	knocked := combat.KnockModule(ship, 0.0, 1.0, rng, knockCfg())
	assert.Empty(t, knocked)
	assert.Len(t, ship.Equipment, 1)
}

// Rebuilding Equipment as a fresh slice leaves any slice that aliased the old
// backing array intact (NFR-003 — the AOI baseline must not be corrupted).
func TestUnit_KnockModule_DoesNotMutateAliasedSlice(t *testing.T) {
	t.Parallel()
	ship := shipWith(
		domain.InstalledEquipment{Type: "up_launcher", Level: 1},
		domain.InstalledEquipment{Type: "up_shield", Level: 1},
	)
	before := ship.Equipment // alias the live slice, as a snapshot would
	rng := &queueRNG{vals: []float64{0.1, 0.0}}
	combat.KnockModule(ship, 0.0, 1.0, rng, knockCfg())
	assert.Equal(t, []domain.InstalledEquipment{
		{Type: "up_launcher", Level: 1},
		{Type: "up_shield", Level: 1},
	}, before, "the previously-aliased slice must be untouched")
}

// A seeded math/rand source makes the roll reproducible: the same seed and the
// same ship yield the same outcome (deterministic for tests, NFR-001).
func TestUnit_KnockModule_SeededRNGIsReproducible(t *testing.T) {
	t.Parallel()
	run := func() []domain.InstalledEquipment {
		ship := shipWith(
			domain.InstalledEquipment{Type: "up_launcher", Level: 1},
			domain.InstalledEquipment{Type: "up_hide", Level: 1},
			domain.InstalledEquipment{Type: "up_shield", Level: 1},
		)
		//nolint:gosec // deterministic test RNG, not security-sensitive
		rng := rand.New(rand.NewSource(42))
		return combat.KnockModule(ship, 0.0, 0.0, rng, knockCfg())
	}
	assert.Equal(t, run(), run())
}

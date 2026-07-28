package sector

import (
	"context"
	"encoding/json"
	"fmt"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// Refitter recomputes a ship's folded stat fields (MaxSpeed, MaxShield,
// EnergyDelta, …) from its current Equipment list. The worker calls it after a
// module is knocked off in combat (TASK-100.3.9.1, FR-A5) so the ship loses that
// module's stat boost. Nil disables the recompute — the knocked module is still
// removed and (for up_shield) the shield still collapses, but the remaining
// stats keep their pre-knock values until the next explicit outfit. Wired in the
// app over balance (ship-class base stats + Equipments).
type Refitter interface {
	Refit(ship *domain.Ship)
}

// WithRefit wires the equipment-refit dependency used after a knockoff.
func WithRefit(r Refitter) Option {
	return func(w *Worker) {
		w.refit = r
	}
}

const shieldModuleType = "up_shield"

// ModuleKnockedTopic is the per-player bus topic a module-knockoff is published
// to (TASK-100.3.9.1). The WS handler subscribes to its own player's topic,
// mirroring PoliceScanTopic / rent.OverdueTopic.
func ModuleKnockedTopic(player domain.PlayerID) string {
	return fmt.Sprintf("module.knocked.%d", int64(player))
}

// ModuleKnockedEvent is broadcast to a ship's owner when one of its modules is
// stripped off by weapon fire (SP DestroyModule). EquipmentType lets the SPA
// name the module from its own catalog for the journal line ("Модуль … уничтожен").
type ModuleKnockedEvent struct {
	PlayerID    domain.PlayerID    `json:"playerId"`
	ShipID      domain.ShipID      `json:"shipId"`
	SectorID    domain.SectorID    `json:"sectorId"`
	EquipmentID domain.EquipmentID `json:"equipmentId"`
	Type        string             `json:"type"`
}

// knockModules runs the DestroyModule roll on a ship that just took a weapon hit
// and survived it (TASK-100.3.9.1, FR-A1). shieldFrac/hullFrac are derived from
// the current pools; a ship with no shield module (MaxShield 0) is treated as
// fully-down (shieldFrac 0), which is exactly when knockoff is most active.
//
// On an actual knockoff it removes the module(s), recomputes the fit (FR-A5),
// collapses the shield when the generator itself was knocked (FR-A6), persists
// the ship immediately and notifies the owner (FR-A5 journal event). No-op when
// nothing is knocked, so the hot combat path pays only the two chance rolls.
func (w *Worker) knockModules(ctx context.Context, s *sectorState, target *domain.Ship) {
	if len(target.Equipment) == 0 {
		return
	}
	shieldFrac := 0.0
	if target.MaxShield > 0 {
		shieldFrac = float64(target.Shield) / float64(target.MaxShield)
	}
	hullFrac := 0.0
	if target.MaxHP > 0 {
		hullFrac = float64(target.HP) / float64(target.MaxHP)
	}

	knocked := combat.KnockModule(target, shieldFrac, hullFrac, w.rng, w.cfg.Knock)
	if len(knocked) == 0 {
		return
	}

	// FR-A6: losing the shield generator is a durable invariant. Set the marker
	// once, when up_shield is knocked off — it stays set across later, unrelated
	// knockoffs (whose refit would otherwise restore the class-base shield) and
	// across cold-start (persisted below).
	if knockedType(knocked, shieldModuleType) {
		target.ShieldGeneratorDestroyed = true
	}

	// Recompute derived flags/stats from the reduced fit (FR-A5).
	target.IsHidden = cloakEngagedFromEquipment(target.Equipment)
	if w.refit != nil {
		w.refit.Refit(target)
	}
	// Consult the marker UNCONDITIONALLY after the refit: a destroyed generator
	// forces MaxShield/Shield to 0 regardless of which module was knocked this
	// event. ChargeShield already no-ops on MaxShield 0, so the shield never
	// regenerates, which is what opens the ship to capture (TASK-100.3.9.4).
	if target.ShieldGeneratorDestroyed {
		target.MaxShield = 0
		target.ShieldRecharge = 0
		target.Shield = 0
	}

	s.markDirty(target.ID)
	// Persist through the equipment path: Save/BatchUpdate write only dynamic
	// fields, so the reduced Equipment, the collapsed max_shield and the marker
	// would be lost at cold-start without this (CRITICAL-1).
	w.immediateSaveEquipment(target)
	// Journal event is for real players only — every NPC ship carries an AI
	// controller (same split as the police scan), and nobody subscribes to the
	// system NPC player's topic.
	if _, isNPC := s.controllers[target.ID]; !isNPC {
		for _, m := range knocked {
			w.publishModuleKnocked(ctx, s, target, m)
		}
	}
}

// knockedType reports whether a module of the given type is in the knocked set.
func knockedType(knocked []domain.InstalledEquipment, typ string) bool {
	for _, m := range knocked {
		if m.Type == typ {
			return true
		}
	}
	return false
}

// publishModuleKnocked emits the per-player knockoff event on the bus. Best-
// effort: a nil bus (pure unit tests), an NPC owner, or a publish error is
// skipped/logged, never blocking the tick. Mirrors publishPoliceScan.
func (w *Worker) publishModuleKnocked(ctx context.Context, s *sectorState, ship *domain.Ship, m domain.InstalledEquipment) {
	if w.bus == nil || ship.PlayerID == 0 {
		return
	}
	payload, err := json.Marshal(ModuleKnockedEvent{
		PlayerID:    ship.PlayerID,
		ShipID:      ship.ID,
		SectorID:    s.sectorID,
		EquipmentID: m.EquipmentID,
		Type:        m.Type,
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "knock: marshal event", "err", err, "player", int64(ship.PlayerID))
		return
	}
	if err := w.publish(ctx, ModuleKnockedTopic(ship.PlayerID), payload); err != nil {
		w.logger.ErrorContext(ctx, "knock: publish event", "err", err, "player", int64(ship.PlayerID))
	}
}

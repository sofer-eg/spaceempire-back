package balance

import (
	"fmt"

	"spaceempire/back/internal/domain"
)

// Equipment is one row of the ct_updates catalog — a ship add-on (engine,
// shield, drill, jump drive, …). Prices/levels are split per ship class
// (one row per class), so the catalog is keyed by the unique id, and the
// same Type recurs across classes. Phase 8.16.
//
// The energy chain: up_generator (EnergyUseType "reverse", Dependance "none")
// feeds up_accumulator ("hold"), on which most modules depend
// (Dependance "up_accumulator").
type Equipment struct {
	ID            domain.EquipmentID
	Type          string // up_engine, up_shield, up_drill, …
	Description   string // human-readable name
	MaxLevel      int    // 0 = single-level
	Race          int    // 0 = universal, else race-restricted
	ShipClass     int    // ct_ship_classes.class this price tier targets (0 = any)
	Price         int64
	PricePerLevel int64
	MinWarRate    int // rank/reputation gates (unused yet; kept for fidelity)
	MinTradeRate  int
	MinRaceRate   int
	IsBase        bool   // base (stock) equipment
	Position      int    // slot: 1 inner, 2 outer
	Dependance    string // "none" or another Type this one switches off with
	EnergyUseType string // always / action / reverse / hold
	EnergyUsage   int
}

// Equipments is the immutable in-memory equipment catalog built from
// configs/equipment.yaml. Build it once at startup and inject read-only.
type Equipments struct {
	byID map[domain.EquipmentID]Equipment
	list []Equipment
}

// NewEquipments validates the input and returns an *Equipments, or an error
// wrapping one of the package sentinels.
func NewEquipments(items []Equipment) (*Equipments, error) {
	byID := make(map[domain.EquipmentID]Equipment, len(items))
	for _, e := range items {
		if e.ID <= 0 {
			return nil, fmt.Errorf("%w: %d", ErrInvalidEquipmentID, e.ID)
		}
		if e.Type == "" {
			return nil, fmt.Errorf("%w: id=%d", ErrEmptyEquipmentType, e.ID)
		}
		if _, dup := byID[e.ID]; dup {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateEquipmentID, e.ID)
		}
		byID[e.ID] = e
	}
	list := make([]Equipment, len(items))
	copy(list, items)
	return &Equipments{byID: byID, list: list}, nil
}

// GetEquipment returns the equipment row for id and true, or zero value and
// false when the id is unknown.
func (c *Equipments) GetEquipment(id domain.EquipmentID) (Equipment, bool) {
	e, ok := c.byID[id]
	return e, ok
}

// AllEquipment returns every loaded equipment row, ordered as the YAML. The
// returned slice is a defensive copy.
func (c *Equipments) AllEquipment() []Equipment {
	out := make([]Equipment, len(c.list))
	copy(out, c.list)
	return out
}

// EquipmentCount is the number of distinct equipment rows loaded.
func (c *Equipments) EquipmentCount() int { return len(c.list) }

// EquipmentByType returns all rows of a given type (one per ship class), in
// YAML order.
func (c *Equipments) EquipmentByType(t string) []Equipment {
	var out []Equipment
	for _, e := range c.list {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// EquipmentByShipClass returns all rows priced for a given ship class, in
// YAML order.
func (c *Equipments) EquipmentByShipClass(class int) []Equipment {
	var out []Equipment
	for _, e := range c.list {
		if e.ShipClass == class {
			out = append(out, e)
		}
	}
	return out
}

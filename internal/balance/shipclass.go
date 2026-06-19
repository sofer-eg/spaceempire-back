package balance

import (
	"fmt"

	"spaceempire/back/internal/domain"
)

// ShipClassCategory groups ship classes into the gameplay roles inherited
// from the original StarWind / X-universe taxonomy. It is derived from the
// ct_ship_classes.class column (see (ShipClass).Category).
type ShipClassCategory string

const (
	CategoryCarrier      ShipClassCategory = "M1" // носитель — capital ship, huge hangar
	CategoryDestroyer    ShipClassCategory = "M2" // эсминец — capital warship
	CategoryHeavyFighter ShipClassCategory = "M3" // тяжёлый истребитель
	CategoryFighter      ShipClassCategory = "M4" // истребитель / перехватчик
	CategoryScout        ShipClassCategory = "M5" // разведчик — fast, cheap
	CategoryCorvette     ShipClassCategory = "M6" // корвет
	CategoryFreighter    ShipClassCategory = "TL" // супертранспорт / носитель-транспорт
	CategorySpecial      ShipClassCategory = "XX" // уникальный / флагман / служебный слот
	CategoryTransport    ShipClassCategory = "TS" // транспорт — грузовой
)

// categoryByClass maps the legacy ct_ship_classes.class column to a category.
// The class column encodes the gameplay role; unique ships (Hyperion, the
// race-100 specials) reuse a base class number, so they inherit its category.
var categoryByClass = map[int]ShipClassCategory{
	1: CategoryCarrier,
	2: CategoryDestroyer,
	3: CategoryHeavyFighter,
	4: CategoryFighter,
	5: CategoryScout,
	6: CategoryCorvette,
	7: CategoryFreighter,
	8: CategorySpecial,
	9: CategoryTransport,
}

// categoryLabel is the human-readable Russian label per category.
var categoryLabel = map[ShipClassCategory]string{
	CategoryCarrier:      "Носитель",
	CategoryDestroyer:    "Эсминец",
	CategoryHeavyFighter: "Тяжёлый истребитель",
	CategoryFighter:      "Истребитель",
	CategoryScout:        "Разведчик",
	CategoryCorvette:     "Корвет",
	CategoryFreighter:    "Супертранспорт",
	CategorySpecial:      "Специальный",
	CategoryTransport:    "Транспорт",
}

// ShipClass is one row of the ct_ship_classes catalog — a buildable ship
// blueprint. Speeds/accelerations are the original per-tick StarWind values
// (the consuming spawn/shipyard phase calibrates them to TickInterval).
type ShipClass struct {
	ID    domain.ShipClassID
	Race  int    // owning race (1..8, 100 = special/unique)
	Type  int    // per-race slot number (1..9 standard, 10+ extra/unique)
	Class int    // gameplay class number → Category
	Name  string // ship name, e.g. "Колосс", "Меркурий"

	Speed           float64
	Acceleration    float64
	Laser           int // laser power budget
	Shield          int // max shield
	Hull            int // max HP
	ShieldCharge    int
	Maneuverability float64
	CargoBay        int
	BasePrice       int64
	// Radar is the personal radar radius in spaceempire world units (phase
	// 10.20). The original ct_ship_classes has no radar column, so the loader
	// defaults it per gameplay category (radarForCategory); ship_classes.yaml
	// may override with an explicit `radar:`. See back/docs/specs/radar.md.
	Radar int

	HangerSmall     int // small-ship hangar capacity
	HangerCapital   int // capital-ship hangar capacity
	HangerShipType  int // hangar slot this ship occupies (1 capital, 2 small)
	HangerShipSpace int // space this ship takes in a hangar
	PilotCabin      int // pilots that fit aboard
	JumpFuel        float64
}

// Category returns the gameplay category for this ship class, or
// CategorySpecial when the class number is unknown.
func (s ShipClass) Category() ShipClassCategory {
	if c, ok := categoryByClass[s.Class]; ok {
		return c
	}
	return CategorySpecial
}

// CategoryLabel returns the Russian label for a category (empty if unknown).
func CategoryLabel(c ShipClassCategory) string { return categoryLabel[c] }

// radarByCategory is the default personal radar radius per gameplay category
// (phase 10.20). Calibrated to the spaceempire sector (radius 5000); all under
// 5000 so visibility is genuinely limited. Scouts see farthest. See
// back/docs/specs/radar.md.
var radarByCategory = map[ShipClassCategory]int{
	CategoryScout:        3500,
	CategoryCorvette:     3000,
	CategoryHeavyFighter: 2800,
	CategoryFighter:      2800,
	CategoryFreighter:    2600,
	CategoryCarrier:      2400,
	CategoryDestroyer:    2400,
	CategoryTransport:    2200,
	CategorySpecial:      2800,
}

// radarDefault is the fallback when a category has no entry.
const radarDefault = 2800

// radarForCategory returns the default radar radius for a category.
func radarForCategory(c ShipClassCategory) int {
	if r, ok := radarByCategory[c]; ok {
		return r
	}
	return radarDefault
}

// ShipClasses is the immutable in-memory ship-class catalog built from
// configs/ship_classes.yaml. Build it once at startup and inject read-only.
type ShipClasses struct {
	byID map[domain.ShipClassID]ShipClass
	list []ShipClass
}

// NewShipClasses validates the input and returns a *ShipClasses, or an error
// wrapping one of the package sentinels.
func NewShipClasses(classes []ShipClass) (*ShipClasses, error) {
	byID := make(map[domain.ShipClassID]ShipClass, len(classes))
	for _, c := range classes {
		if c.ID <= 0 {
			return nil, fmt.Errorf("%w: %d", ErrInvalidShipClassID, c.ID)
		}
		if c.Name == "" {
			return nil, fmt.Errorf("%w: id=%d", ErrEmptyShipClassName, c.ID)
		}
		if _, dup := byID[c.ID]; dup {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateShipClassID, c.ID)
		}
		byID[c.ID] = c
	}
	list := make([]ShipClass, len(classes))
	copy(list, classes)
	return &ShipClasses{byID: byID, list: list}, nil
}

// GetShipClass returns the ship class for id and true, or zero value and
// false when the id is unknown.
func (c *ShipClasses) GetShipClass(id domain.ShipClassID) (ShipClass, bool) {
	s, ok := c.byID[id]
	return s, ok
}

// HangerOf returns the hangar capacity/footprint of a ship class as a
// domain.Hanger (phase 10.3.24), so the sector worker can gate ship-to-ship
// docking without depending on the balance package. An unknown class id (0 =
// spacesuit/legacy, or a missing row) yields the zero Hanger — no capacity and
// no footprint, which rejects ship-to-ship docking for that ship.
func (c *ShipClasses) HangerOf(id domain.ShipClassID) domain.Hanger {
	s, ok := c.byID[id]
	if !ok {
		return domain.Hanger{}
	}
	return domain.Hanger{
		Capital:   s.HangerCapital,
		Small:     s.HangerSmall,
		ShipType:  s.HangerShipType,
		ShipSpace: s.HangerShipSpace,
	}
}

// AllShipClasses returns every loaded ship class, ordered as the YAML. The
// returned slice is a defensive copy.
func (c *ShipClasses) AllShipClasses() []ShipClass {
	out := make([]ShipClass, len(c.list))
	copy(out, c.list)
	return out
}

// ShipClassCount is the number of distinct ship classes loaded.
func (c *ShipClasses) ShipClassCount() int { return len(c.list) }

// ShipClassesByRace returns the classes owned by a race, in YAML order.
func (c *ShipClasses) ShipClassesByRace(race int) []ShipClass {
	var out []ShipClass
	for _, s := range c.list {
		if s.Race == race {
			out = append(out, s)
		}
	}
	return out
}

// ScoutForRace returns the race's M5 (scout) class — the starter ship model
// (phase 10.10). Returns the first M5 in YAML order and true, or zero value
// and false when the race has no scout class.
func (c *ShipClasses) ScoutForRace(race int) (ShipClass, bool) {
	for _, s := range c.list {
		if s.Race == race && s.Category() == CategoryScout {
			return s, true
		}
	}
	return ShipClass{}, false
}

// TSForRace returns the race's TS (transport) class name — used to label NPC
// trader/miner/passenger ships. Returns the first TS in YAML order and true,
// or zero value and false when the race has no transport class.
func (c *ShipClasses) TSForRace(race int) (ShipClass, bool) {
	for _, s := range c.list {
		if s.Race == race && s.Category() == CategoryTransport {
			return s, true
		}
	}
	return ShipClass{}, false
}

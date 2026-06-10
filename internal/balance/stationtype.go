package balance

import "fmt"

// StationKind is the station_types.type column: what the object is, distinct
// from its production recipe. Ported from the original StarWind comment.
type StationKind int

const (
	StationKindTradeStation StationKind = 0 // ТС
	StationKindShipyard     StationKind = 1 // верфь
	StationKindFactory      StationKind = 2 // простая станция (фабрика)
	StationKindRebuildable  StationKind = 3 // перестраиваемая станция
)

// stationKindLabel is the human-readable Russian label per kind.
var stationKindLabel = map[StationKind]string{
	StationKindTradeStation: "Торговая станция",
	StationKindShipyard:     "Верфь",
	StationKindFactory:      "Фабрика",
	StationKindRebuildable:  "Перестраиваемая станция",
}

// Label returns the Russian label for a station kind (empty if unknown).
func (k StationKind) Label() string { return stationKindLabel[k] }

// StationType is one row of the station_types catalog — a station blueprint
// (name + kind + race + durability). The production recipe keyed by the same
// id lives separately (see Recipe / configs/station_types.yaml). Phase 8.15.
type StationType struct {
	ID       int
	Name     string
	RaceID   int         // 0 = base/neutral, 1..8 = racial variant
	Kind     StationKind // station_types.type (0..3)
	Hull     int
	Shield   int
	Sellable bool
}

// StationTypes is the immutable in-memory station-type catalog built from
// configs/station_types.yaml. Build it once at startup and inject read-only.
type StationTypes struct {
	byID map[int]StationType
	list []StationType
}

// NewStationTypes validates the input and returns a *StationTypes, or an error
// wrapping one of the package sentinels. Station type id 0 is valid (the
// "under construction" type), so ids must only be non-negative.
func NewStationTypes(types []StationType) (*StationTypes, error) {
	byID := make(map[int]StationType, len(types))
	for _, t := range types {
		if t.ID < 0 {
			return nil, fmt.Errorf("%w: %d", ErrInvalidStationTypeID, t.ID)
		}
		if t.Name == "" {
			return nil, fmt.Errorf("%w: id=%d", ErrEmptyStationTypeName, t.ID)
		}
		if _, dup := byID[t.ID]; dup {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateStationTypeID, t.ID)
		}
		byID[t.ID] = t
	}
	list := make([]StationType, len(types))
	copy(list, types)
	return &StationTypes{byID: byID, list: list}, nil
}

// GetStationType returns the station type for id and true, or zero value and
// false when the id is unknown.
func (c *StationTypes) GetStationType(id int) (StationType, bool) {
	t, ok := c.byID[id]
	return t, ok
}

// AllStationTypes returns every loaded station type, ordered as the YAML. The
// returned slice is a defensive copy.
func (c *StationTypes) AllStationTypes() []StationType {
	out := make([]StationType, len(c.list))
	copy(out, c.list)
	return out
}

// StationTypeCount is the number of distinct station types loaded.
func (c *StationTypes) StationTypeCount() int { return len(c.list) }

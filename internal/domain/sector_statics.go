package domain

// SectorStatics bundles the static, cold-start sector objects. The first
// four kinds are dockable; LaserTowers are not dockable but ride the same
// load-once/render-once path (they are read-only this phase — see
// back/docs/specs/lasertowers.md). Loaded once at worker cold start;
// passed by value through the worker's snapshot so subscribers see a
// stable per-tick view.
type SectorStatics struct {
	Stations      []Station
	Shipyards     []Shipyard
	TradeStations []TradeStation
	Pirbases      []Pirbase
	LaserTowers   []LaserTower
	Satellites    []Satellite
}

// IsEmpty reports whether every slice is empty.
func (s SectorStatics) IsEmpty() bool {
	return len(s.Stations) == 0 && len(s.Shipyards) == 0 &&
		len(s.TradeStations) == 0 && len(s.Pirbases) == 0 &&
		len(s.LaserTowers) == 0 && len(s.Satellites) == 0
}

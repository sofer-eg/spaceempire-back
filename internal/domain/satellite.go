package domain

// Satellite is a player-deployed navigation satellite (phase 10.15): a static
// sector object that doubles as a destructible combat target and a sector
// radar beacon. It rides the same load-once/render-once path as the laser
// tower (see back/docs/specs/satellite.md). HP/Shield live in the worker's
// per-sector destructibles map (combat state); the fields here are the
// cold-start values and the immutable layout (owner/position/race).
type Satellite struct {
	ID       SatelliteID
	OwnerID  *PlayerID
	SectorID SectorID
	Pos      Vec2
	Race     int
	Built    bool

	HP             int
	Shield         int
	MaxShield      int
	ShieldRecharge int
}

// ObjectID returns the satellite's EntityRef, used as the key in the sector
// destructibles map and the L2 big-radar diff.
func (s Satellite) ObjectID() EntityRef {
	return EntityRef{Kind: EntityKindSatellite, ID: int64(s.ID)}
}

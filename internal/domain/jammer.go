package domain

// Jammer is a player-deployed hyper-interference generator (TASK-131, port of
// SP ct_drones class 7 "Генератор гипер-помех"): a static sector object that
// doubles as a destructible combat target and a no-jump field — while it lives,
// no ship within Config.JammerRange may use its seamless jump drive. It rides
// the same load-once/render-once path as the navigation satellite (see
// back/docs/specs/jammer.md). HP/Shield live in the worker's per-sector
// destructibles map (combat state); the fields here are the cold-start values
// and the immutable layout (owner/position/race).
type Jammer struct {
	ID       JammerID
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

// ObjectID returns the jammer's EntityRef, used as the key in the sector
// destructibles map and the L2 big-radar diff.
func (j Jammer) ObjectID() EntityRef {
	return EntityRef{Kind: EntityKindJammer, ID: int64(j.ID)}
}

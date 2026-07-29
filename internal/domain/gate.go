package domain

// Gate is a bidirectional jump point between two sectors. A ship entering
// at PosA in SectorA emerges at PosB in SectorB, and vice versa.
//
// Since TASK-110 a gate is also destructible. The combat fields below are the
// cold-start values shared by both of its endpoints: each linked sector's worker
// registers its own side as a DestructibleStatic and damages that copy, so the
// two sides take damage independently (one writer per sector — they cannot share
// a live HP pool). Destroying EITHER side destroys the gate: the row is flagged
// and the link disappears from the topology, which is what both workers, the
// router and the jump commands read.
type Gate struct {
	ID      GateID
	SectorA SectorID
	PosA    Vec2
	SectorB SectorID
	PosB    Vec2

	HP             int
	Shield         int
	MaxShield      int
	ShieldRecharge int
	// Destroyed is the persisted wreck flag. A destroyed gate is not loaded into
	// the topology at all (its link is gone for good until someone repairs it —
	// TASK-67 owns repair), so nothing downstream has to filter on it.
	Destroyed bool
}

// ObjectID returns the gate's EntityRef — the key both sectors use for their
// endpoint's combat state.
func (g Gate) ObjectID() EntityRef {
	return EntityRef{Kind: EntityKindGate, ID: int64(g.ID)}
}

// EndpointPos returns the gate's position inside the given sector and whether the
// sector is one of its endpoints at all.
func (g Gate) EndpointPos(sectorID SectorID) (Vec2, bool) {
	switch sectorID {
	case g.SectorA:
		return g.PosA, true
	case g.SectorB:
		return g.PosB, true
	default:
		return Vec2{}, false
	}
}

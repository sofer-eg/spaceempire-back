package domain

// Gate is a bidirectional jump point between two sectors. A ship entering
// at PosA in SectorA emerges at PosB in SectorB, and vice versa.
type Gate struct {
	ID      GateID
	SectorA SectorID
	PosA    Vec2
	SectorB SectorID
	PosB    Vec2
}

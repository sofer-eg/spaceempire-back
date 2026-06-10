package domain

// Shipyard — верфь, где строятся и ремонтируются корабли. Статичный
// объект сектора, цель стыковки. Логика постройки кораблей — фаза 3+.
type Shipyard struct {
	ID             ShipyardID
	OwnerID        *PlayerID
	SectorID       SectorID
	Pos            Vec2
	HP             int
	Shield         int
	MaxShield      int
	ShieldRecharge int
	Race           int
	Built          bool
}

func (s Shipyard) ObjectID() EntityRef    { return EntityRef{Kind: EntityKindShipyard, ID: int64(s.ID)} }
func (s Shipyard) ObjectSector() SectorID { return s.SectorID }
func (s Shipyard) ObjectPos() Vec2        { return s.Pos }

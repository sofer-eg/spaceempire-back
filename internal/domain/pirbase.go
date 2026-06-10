package domain

// Pirbase — пиратская база. Статичный объект сектора, цель стыковки.
// Механика продажи рабов на пирбазах — фаза 5.6.
type Pirbase struct {
	ID             PirbaseID
	SectorID       SectorID
	Pos            Vec2
	HP             int
	Shield         int
	MaxShield      int
	ShieldRecharge int
	Angle          float64
	Race           int
	Built          bool
}

func (p Pirbase) ObjectID() EntityRef    { return EntityRef{Kind: EntityKindPirbase, ID: int64(p.ID)} }
func (p Pirbase) ObjectSector() SectorID { return p.SectorID }
func (p Pirbase) ObjectPos() Vec2        { return p.Pos }

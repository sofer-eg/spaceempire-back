package domain

import "time"

// Station — производственная фабрика. Статичный объект сектора, занимает
// фиксированную позицию, является целью стыковки. Аренда и налоги
// добавляются в фазе 6.4.
type Station struct {
	ID       StationID
	OwnerID  *PlayerID
	Type     int
	SectorID SectorID
	Pos      Vec2
	HP       int
	Shield   int
	// MaxShield / ShieldRecharge drive the per-tick shield recharge of a
	// destructible static (phase 6.2b). MaxShield is the cap; ShieldRecharge
	// is added each tick until full.
	MaxShield      int
	ShieldRecharge int
	Race           int
	Built          bool

	// InProgress / NextCycleAt — состояние производственного цикла
	// (см. internal/economy/production). NextCycleAt zero, когда цикл
	// не запущен.
	InProgress  bool
	NextCycleAt time.Time
}

func (s Station) ObjectID() EntityRef    { return EntityRef{Kind: EntityKindStation, ID: int64(s.ID)} }
func (s Station) ObjectSector() SectorID { return s.SectorID }
func (s Station) ObjectPos() Vec2        { return s.Pos }

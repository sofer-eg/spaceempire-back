package domain

// TradeStation — торговая станция сектора. В старой схеме UNIQUE по
// сектору (один TS на сектор), в новой версии этот инвариант пока не
// форсим: первый seed следует этому правилу, обеспечивать на уровне БД
// или бизнес-логики будем при появлении соответствующей механики
// (фаза 3.4).
type TradeStation struct {
	ID             TradeStationID
	OwnerID        *PlayerID
	Type           int
	SectorID       SectorID
	Pos            Vec2
	HP             int
	Shield         int
	MaxShield      int
	ShieldRecharge int
	Race           int
	Built          bool
}

func (t TradeStation) ObjectID() EntityRef {
	return EntityRef{Kind: EntityKindTradeStation, ID: int64(t.ID)}
}
func (t TradeStation) ObjectSector() SectorID { return t.SectorID }
func (t TradeStation) ObjectPos() Vec2        { return t.Pos }

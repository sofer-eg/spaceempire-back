package domain

// DockableObject — общий контракт статичных объектов сектора, к которым
// может пристыковаться корабль. Реализуется Station, Shipyard,
// TradeStation, Pirbase. Сигнатура методов начинается с Object*,
// чтобы не конфликтовать с возможными собственными ID/Pos в типах
// и читалось как «объектная идентичность».
type DockableObject interface {
	ObjectID() EntityRef
	ObjectSector() SectorID
	ObjectPos() Vec2
}

// Hanger — проекция hanger-полей balance.ShipClass для проверки стыковки
// корабль-к-кораблю (phase 10.3.24, порт SP Docking op=2 target_type=5).
// Capital/Small — вместимость ангара носителя; ShipType — слот, который
// корабль занимает у носителя (1 = capital, 2 = small, 0 = не помещается
// в ангар вообще); ShipSpace — место, занимаемое кораблём в ангаре.
type Hanger struct {
	Capital   int
	Small     int
	ShipType  int
	ShipSpace int
}

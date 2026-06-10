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

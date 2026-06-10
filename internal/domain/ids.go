package domain

type (
	ShipID         int64
	SectorID       int64
	PlayerID       int64
	GateID         int64
	StationID      int64
	ShipyardID     int64
	TradeStationID int64
	PirbaseID      int64
	LaserTowerID   int64
	SatelliteID    int64
	ContainerID    int64
	ClanID         int64
	ShipClassID    int64
	RaceID         int16
	EquipmentID    int64
)

type EntityKind uint8

const (
	EntityKindUnknown      EntityKind = 0
	EntityKindShip         EntityKind = 1
	EntityKindStation      EntityKind = 2
	EntityKindShipyard     EntityKind = 3
	EntityKindTradeStation EntityKind = 4
	EntityKindPirbase      EntityKind = 5
	EntityKindDrone        EntityKind = 6
	EntityKindLaserTower   EntityKind = 7
	EntityKindContainer    EntityKind = 8
	// Player and Clan are not in-world objects but are valid relation
	// endpoints (phase 6.2): relations are keyed by EntityRef, so a player
	// or a clan needs a kind. Race joins this list in 5.2.
	EntityKindPlayer EntityKind = 9
	EntityKindClan   EntityKind = 10
	// EntityKindSatellite is a player-deployed navigation satellite (phase
	// 10.15): a destructible static beacon that reveals the sector radar.
	EntityKindSatellite EntityKind = 11
)

type EntityRef struct {
	Kind EntityKind
	ID   int64
}

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
	JammerID       int64
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
	// EntityKindTorpedo is an in-flight homing torpedo (phase 10.3.5): a
	// persistent, shoot-downable projectile. Unlike a fire-and-forget missile a
	// torpedo carries its own HP, so it joins the targetable universe — a weapon
	// can lock onto and destroy an incoming torpedo (ЧТЗ doc-1 §3 FR-008). See
	// isProjectileTargetKind, the single point that makes it a weapon target.
	EntityKindTorpedo EntityKind = 12
	// EntityKindJammer is a player-deployed hyper-interference generator
	// (TASK-131, SP ct_drones class 7 "Генератор гипер-помех"): a destructible
	// static that jams the seamless jump drive of every ship within
	// Config.JammerRange. Same static machinery as the satellite.
	EntityKindJammer EntityKind = 13
	// EntityKindGate is a jump gate (TASK-110). Gates were the one static
	// excluded from the weapon-target set (ЧТЗ C-04); they now carry combat
	// state like the rest, with one difference that shapes everything about
	// them: a gate has TWO endpoints, one in each linked sector, and each
	// sector's worker owns only its own side. The row — and the link — is
	// shared, so destroying either endpoint destroys the gate and severs the
	// link (see world.Topology.DestroyGate and sector/gate_combat.go).
	EntityKindGate EntityKind = 14
)

type EntityRef struct {
	Kind EntityKind
	ID   int64
}

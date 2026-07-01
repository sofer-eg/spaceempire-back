package dto

import "spaceempire/back/internal/domain"

// Station mirrors domain.Station on the wire. Static objects are sent once
// when a WS client subscribes (separate from the ship patch stream) and
// included in HTTP /api/state for non-WS clients.
type Station struct {
	ID       int64   `json:"id"`
	OwnerID  *int64  `json:"ownerID,omitempty"`
	Type     int     `json:"type"`
	SectorID int64   `json:"sectorID"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	HP       int     `json:"hp"`
	Shield   int     `json:"shield"`
	Race     int     `json:"race"`
	Built    bool    `json:"built"`
}

type Shipyard struct {
	ID       int64   `json:"id"`
	OwnerID  *int64  `json:"ownerID,omitempty"`
	SectorID int64   `json:"sectorID"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	HP       int     `json:"hp"`
	Shield   int     `json:"shield"`
	Race     int     `json:"race"`
	Built    bool    `json:"built"`
}

type TradeStation struct {
	ID       int64   `json:"id"`
	OwnerID  *int64  `json:"ownerID,omitempty"`
	Type     int     `json:"type"`
	SectorID int64   `json:"sectorID"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	HP       int     `json:"hp"`
	Shield   int     `json:"shield"`
	Race     int     `json:"race"`
	Built    bool    `json:"built"`
}

type Pirbase struct {
	ID       int64   `json:"id"`
	SectorID int64   `json:"sectorID"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	HP       int     `json:"hp"`
	Shield   int     `json:"shield"`
	Angle    float64 `json:"angle"`
	Race     int     `json:"race"`
	Built    bool    `json:"built"`
}

type LaserTower struct {
	ID       int64   `json:"id"`
	OwnerID  *int64  `json:"ownerID,omitempty"`
	SectorID int64   `json:"sectorID"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	HP       int     `json:"hp"`
	Shield   int     `json:"shield"`
	Race     int     `json:"race"`
	Built    bool    `json:"built"`
}

type Satellite struct {
	ID       int64   `json:"id"`
	OwnerID  *int64  `json:"ownerID,omitempty"`
	SectorID int64   `json:"sectorID"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	HP       int     `json:"hp"`
	Shield   int     `json:"shield"`
	Race     int     `json:"race"`
	Built    bool    `json:"built"`
}

// SectorStatics bundles every static object kind of a sector for transport
// over the wire. Empty slices are omitted via omitempty so a sector without
// pirbases (most of them) won't ship a "pirbases":[] noise line.
type SectorStatics struct {
	Stations      []Station      `json:"stations,omitempty"`
	Shipyards     []Shipyard     `json:"shipyards,omitempty"`
	TradeStations []TradeStation `json:"tradeStations,omitempty"`
	Pirbases      []Pirbase      `json:"pirbases,omitempty"`
	LaserTowers   []LaserTower   `json:"laserTowers,omitempty"`
	Satellites    []Satellite    `json:"satellites,omitempty"`
}

// StaticsMessage is the dedicated WS frame the server pushes once per
// subscribe with the current sector's static objects. After this single
// message, the regular Snapshot delta stream takes over.
//
// TickIntervalMs carries the engine tick period in milliseconds so the
// SPA can drive its client-side interpolation without hard-coding the
// value — the server stays the single source of truth for tick rate.
//
// SectorBoundsRadius and NearZoomRadius are rendering-only constants:
// the half-extent of the sector box (±5000 default) and the Near-zoom
// half-side around the player's own ship (500 default). Surfaced here
// so the SPA doesn't duplicate them.
type StaticsMessage struct {
	Type               string  `json:"type"`
	SectorID           int64   `json:"sectorID"`
	TickIntervalMs     int64   `json:"tickIntervalMs"`
	SectorBoundsRadius float64 `json:"sectorBoundsRadius"`
	NearZoomRadius     float64 `json:"nearZoomRadius"`
	// DockRange / GateRange let the SPA gate the dock/jump menu items
	// in TargetsPanel on the same constants the worker validates
	// against — keeps the affordance honest without a roundtrip per
	// hover.
	DockRange float64       `json:"dockRange"`
	GateRange float64       `json:"gateRange"`
	Statics   SectorStatics `json:"statics"`
}

func StationFromDomain(s domain.Station) Station {
	out := Station{
		ID:       int64(s.ID),
		Type:     s.Type,
		SectorID: int64(s.SectorID),
		X:        s.Pos.X,
		Y:        s.Pos.Y,
		HP:       s.HP,
		Shield:   s.Shield,
		Race:     s.Race,
		Built:    s.Built,
	}
	if s.OwnerID != nil {
		o := int64(*s.OwnerID)
		out.OwnerID = &o
	}
	return out
}

func ShipyardFromDomain(s domain.Shipyard) Shipyard {
	out := Shipyard{
		ID:       int64(s.ID),
		SectorID: int64(s.SectorID),
		X:        s.Pos.X,
		Y:        s.Pos.Y,
		HP:       s.HP,
		Shield:   s.Shield,
		Race:     s.Race,
		Built:    s.Built,
	}
	if s.OwnerID != nil {
		o := int64(*s.OwnerID)
		out.OwnerID = &o
	}
	return out
}

func TradeStationFromDomain(t domain.TradeStation) TradeStation {
	out := TradeStation{
		ID:       int64(t.ID),
		Type:     t.Type,
		SectorID: int64(t.SectorID),
		X:        t.Pos.X,
		Y:        t.Pos.Y,
		HP:       t.HP,
		Shield:   t.Shield,
		Race:     t.Race,
		Built:    t.Built,
	}
	if t.OwnerID != nil {
		o := int64(*t.OwnerID)
		out.OwnerID = &o
	}
	return out
}

func PirbaseFromDomain(p domain.Pirbase) Pirbase {
	return Pirbase{
		ID:       int64(p.ID),
		SectorID: int64(p.SectorID),
		X:        p.Pos.X,
		Y:        p.Pos.Y,
		HP:       p.HP,
		Shield:   p.Shield,
		Angle:    p.Angle,
		Race:     p.Race,
		Built:    p.Built,
	}
}

func LaserTowerFromDomain(t domain.LaserTower) LaserTower {
	out := LaserTower{
		ID:       int64(t.ID),
		SectorID: int64(t.SectorID),
		X:        t.Pos.X,
		Y:        t.Pos.Y,
		HP:       t.HP,
		Shield:   t.Shield,
		Race:     t.Race,
		Built:    t.Built,
	}
	if t.OwnerID != nil {
		o := int64(*t.OwnerID)
		out.OwnerID = &o
	}
	return out
}

func SatelliteFromDomain(s domain.Satellite) Satellite {
	out := Satellite{
		ID:       int64(s.ID),
		SectorID: int64(s.SectorID),
		X:        s.Pos.X,
		Y:        s.Pos.Y,
		HP:       s.HP,
		Shield:   s.Shield,
		Race:     s.Race,
		Built:    s.Built,
	}
	if s.OwnerID != nil {
		o := int64(*s.OwnerID)
		out.OwnerID = &o
	}
	return out
}

func StaticsFromDomain(s domain.SectorStatics) SectorStatics {
	out := SectorStatics{}
	if len(s.Stations) > 0 {
		out.Stations = make([]Station, len(s.Stations))
		for i, st := range s.Stations {
			out.Stations[i] = StationFromDomain(st)
		}
	}
	if len(s.Shipyards) > 0 {
		out.Shipyards = make([]Shipyard, len(s.Shipyards))
		for i, sy := range s.Shipyards {
			out.Shipyards[i] = ShipyardFromDomain(sy)
		}
	}
	if len(s.TradeStations) > 0 {
		out.TradeStations = make([]TradeStation, len(s.TradeStations))
		for i, ts := range s.TradeStations {
			out.TradeStations[i] = TradeStationFromDomain(ts)
		}
	}
	if len(s.Pirbases) > 0 {
		out.Pirbases = make([]Pirbase, len(s.Pirbases))
		for i, pb := range s.Pirbases {
			out.Pirbases[i] = PirbaseFromDomain(pb)
		}
	}
	if len(s.LaserTowers) > 0 {
		out.LaserTowers = make([]LaserTower, len(s.LaserTowers))
		for i, lt := range s.LaserTowers {
			out.LaserTowers[i] = LaserTowerFromDomain(lt)
		}
	}
	if len(s.Satellites) > 0 {
		out.Satellites = make([]Satellite, len(s.Satellites))
		for i, sat := range s.Satellites {
			out.Satellites[i] = SatelliteFromDomain(sat)
		}
	}
	return out
}

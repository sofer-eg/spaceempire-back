package dto

// DockRequest is the wire payload for POST /api/cmd/dock. Target.Kind
// must be one of the four static EntityKind values (station=2, shipyard=3,
// trade_station=4, pirbase=5); other kinds are rejected with 400.
type DockRequest struct {
	ShipID int64     `json:"shipID"`
	Target EntityRef `json:"target"`
}

type DockResponse struct {
	OK bool `json:"ok"`
}

// UndockRequest is the wire payload for POST /api/cmd/undock.
type UndockRequest struct {
	ShipID int64 `json:"shipID"`
}

type UndockResponse struct {
	OK bool `json:"ok"`
}

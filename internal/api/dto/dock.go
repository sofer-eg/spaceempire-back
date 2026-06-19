package dto

// DockRequest is the wire payload for POST /api/cmd/dock. Target.Kind must be
// one of the four static EntityKind values (station=2, shipyard=3,
// trade_station=4, pirbase=5) or a host ship (ship=1, phase 10.3.24); other
// kinds are rejected with 400.
type DockRequest struct {
	ShipID int64     `json:"shipID"`
	Target EntityRef `json:"target"`
}

type DockResponse struct {
	OK bool `json:"ok"`
}

// ExternalDockRequest is the wire payload for POST /api/cmd/exdock (phase
// 10.3.23): start the up_exdocking external-docking process toward a host ship
// (Target.Kind must be ship=1). Requires an installed up_exdocking module.
type ExternalDockRequest struct {
	ShipID int64     `json:"shipID"`
	Target EntityRef `json:"target"`
}

type ExternalDockResponse struct {
	OK bool `json:"ok"`
}

// UndockRequest is the wire payload for POST /api/cmd/undock.
type UndockRequest struct {
	ShipID int64 `json:"shipID"`
}

type UndockResponse struct {
	OK bool `json:"ok"`
}

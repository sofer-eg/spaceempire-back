package dto

// SetCourseRequest arms the player's autopilot. The server resolves the
// target via PathRouter and the ship hops through gates automatically.
//
// Approach, when set, asks the autopilot to park at DockRange/2 from the
// referenced static once it reaches the destination sector and wait for
// an explicit /api/cmd/dock. The ship will not dock automatically.
type SetCourseRequest struct {
	ShipID   int64      `json:"shipID"`
	SectorID int64      `json:"sectorID"`
	X        float64    `json:"x"`
	Y        float64    `json:"y"`
	Approach *EntityRef `json:"approach,omitempty"`
}

// SetCourseResponse returns the precomputed route length so the SPA can
// display ETA without re-running BFS client-side.
type SetCourseResponse struct {
	Hops int `json:"hops"`
}

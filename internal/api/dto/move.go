package dto

type MoveRequest struct {
	ShipID int64   `json:"shipID"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	// TargetRef, when set, names the entity the player explicitly clicked
	// (ship/station/shipyard/trade_station/pirbase). The worker records it
	// on the ship so the SPA can paint a persistent "current target"
	// highlight, distinct from hover. Omitting it (or nil) is a bare
	// point-and-click and clears any prior highlight ref.
	TargetRef *EntityRef `json:"targetRef,omitempty"`
}

type MoveResponse struct {
	OK bool `json:"ok"`
}

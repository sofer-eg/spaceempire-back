package dto

// InstallJammerRequest is the POST /api/cmd/install-jammer body: deploy a
// hyper-interference generator (TASK-131) from ShipID's cargo at the ship's
// current position.
type InstallJammerRequest struct {
	ShipID int64 `json:"shipID"`
}

// InstallJammerResponse acknowledges a successful install and returns the new
// jammer id so the SPA can reference it.
type InstallJammerResponse struct {
	OK       bool  `json:"ok"`
	JammerID int64 `json:"jammerID"`
}

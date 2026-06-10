package dto

// InstallSatelliteRequest is the POST /api/cmd/install-satellite body: deploy a
// navigation satellite (phase 10.15) from ShipID's cargo at the ship's current
// position.
type InstallSatelliteRequest struct {
	ShipID int64 `json:"shipID"`
}

// InstallSatelliteResponse acknowledges a successful install and returns the
// new satellite id so the SPA can reference it.
type InstallSatelliteResponse struct {
	OK          bool  `json:"ok"`
	SatelliteID int64 `json:"satelliteID"`
}

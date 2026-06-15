package dto

// MineRequest is the body of POST /api/cmd/mine (phase 10.3.6). PlayerID is
// sourced from the session cookie; the body carries the drilling ship and the
// target asteroid. AsteroidID == 0 is a stop request — it clears any active
// mining mode on the ship.
type MineRequest struct {
	ShipID     int64 `json:"shipID"`
	AsteroidID int64 `json:"asteroidID"`
}

// MineResponse acknowledges that the mining mode was armed (or stopped).
type MineResponse struct {
	OK bool `json:"ok"`
}

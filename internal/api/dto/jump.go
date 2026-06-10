package dto

// JumpRequest fires the player-issued sector hop via /api/cmd/jump. The
// worker validates ownership, gate identity and proximity (Config.GateRange)
// before executing the handoff.
type JumpRequest struct {
	ShipID int64 `json:"shipID"`
	GateID int64 `json:"gateID"`
}

// JumpResponse acknowledges the jump command was accepted. Empty for now —
// the WS snapshot reflects the ship's new sector immediately after.
type JumpResponse struct {
	OK bool `json:"ok"`
}

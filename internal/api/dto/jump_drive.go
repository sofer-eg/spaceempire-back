package dto

// JumpDriveRequest fires the player-issued seamless (gateless) sector jump via
// POST /api/cmd/jump-drive (TASK-100.3.7, port of SP DoJump mode 0). The worker
// validates ownership, the installed up_jump_drive, a working shield generator,
// the real-time cooldown and the forbidden-sector list before folding the ship
// to TargetSectorID.
type JumpDriveRequest struct {
	ShipID         int64 `json:"shipID"`
	TargetSectorID int64 `json:"targetSectorID"`
}

// JumpDriveResponse acknowledges the jump was accepted. Empty for now — the WS
// snapshot reflects the ship's new sector immediately after.
type JumpDriveResponse struct {
	OK bool `json:"ok"`
}

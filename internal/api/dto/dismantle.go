package dto

// DismantleStaticRequest is the POST /api/cmd/dismantle-static body: fold the
// deployed object named by Target (a hyper-interference generator or a navigation
// satellite the player owns) back into ShipID's hold (TASK-146).
type DismantleStaticRequest struct {
	ShipID int64     `json:"shipID"`
	Target EntityRef `json:"target"`
}

// DismantleStaticResponse acknowledges a successful take-down. The object is gone
// from the sector and one goods unit is back in the hold; the SPA re-reads the
// hold and the radar drops the object on the next delta.
type DismantleStaticResponse struct {
	OK bool `json:"ok"`
}

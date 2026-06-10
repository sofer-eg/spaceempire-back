package dto

// AttackRequest is the body of POST /api/cmd/attack. PlayerID comes
// from the session cookie (see auth.RequireAuth); the body only carries
// the attacker ship and the target.
type AttackRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
}

// CeaseFireRequest is the body of POST /api/cmd/cease-fire.
type CeaseFireRequest struct {
	ShipID int64 `json:"shipID"`
}

// AttackResponse / CeaseFireResponse are deliberately minimal — the
// client only needs to know the worker accepted the command.
type AttackResponse struct {
	OK bool `json:"ok"`
}

type CeaseFireResponse struct {
	OK bool `json:"ok"`
}

package dto

// HackRequest is the body of POST /api/cmd/hack (TASK-100.3.9.3, SP UseHack).
// PlayerID comes from the session cookie; the body only carries the hacker ship
// and the trade-station target.
type HackRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
}

// HackResponse echoes how much was stolen so the SPA can log it optimistically;
// the authoritative journal line arrives on the WS station_hacked frame.
type HackResponse struct {
	OK     bool  `json:"ok"`
	Robbed int64 `json:"robbed"`
}

package dto

// CaptureRequest is the body of POST /api/cmd/capture (TASK-100.3.9.4, SP
// DoCapture). PlayerID comes from the session cookie; the body only carries the
// attacker ship and the ship target.
type CaptureRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
}

// CaptureResponse echoes whether the ship was seized so the SPA can log it
// optimistically; the authoritative journal line arrives on the WS ship_capture
// frame.
type CaptureResponse struct {
	OK       bool `json:"ok"`
	Captured bool `json:"captured"`
}

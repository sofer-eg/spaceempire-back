package dto

// LaunchTorpedoRequest is the body of POST /api/cmd/launch-torpedo. PlayerID
// comes from the session cookie; the body carries the launching ship, the
// target, and the ammunition Class (2 = gt23 "Огненная Буря", 3 = gt24
// "Святая Торпеда").
type LaunchTorpedoRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
	Class     int       `json:"class"`
}

// LaunchTorpedoResponse echoes the torpedo id allocated by the worker so the
// client can correlate WS frames with its own UI state. The torpedo spawn is a
// later sub-task (TASK-100.3.5.4); until then a successful launch only enforces
// the gates and the energy cost, so TorpedoID is zero.
type LaunchTorpedoResponse struct {
	OK        bool  `json:"ok"`
	TorpedoID int64 `json:"torpedoID"`
}

package domain

// AIState is the persisted state of one NPC's AI controller (phase 5.1).
// The presence of a row marks a ship as AI-controlled: ControllerKind names
// the controller to instantiate at cold-start, StateJSON carries that
// controller's serialized mutable state (route progress, current target, …)
// so a restart resumes mid-decision instead of from scratch.
//
// One row per ship (ShipID is the primary key). SectorID is denormalized
// from the ship so the worker can load AI state per sector at cold-start,
// the same way drones/containers are loaded.
type AIState struct {
	ShipID         ShipID
	SectorID       SectorID
	ControllerKind string
	// StateJSON is the controller's serialized state as raw JSON. Empty
	// means "no state yet" — the controller starts from its zero value.
	StateJSON []byte
}

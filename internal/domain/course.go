package domain

// Course is an autopilot destination: a sector plus a position inside that
// sector. The autopilot resolver translates a Course into the ship's
// per-tick movement Target, hop by hop through the gate graph.
//
// Approach, when set, signals that the player wants the autopilot to
// park the ship at DockRange/2 from the referenced static once it
// reaches the destination sector. The autopilot does NOT dock — that
// requires an explicit DockCommand from the player.
type Course struct {
	Sector   SectorID
	Pos      Vec2
	Approach *EntityRef
}

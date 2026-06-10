package dto

// Snapshot is the message the WS server pushes to a subscribed client.
// In phase 1.4 the wire format moved to a delta: the first push after
// subscribe carries the entire visible state in Added, and every
// subsequent push contains only the diff from the previous tick. Ships
// is kept (in addition to Added/Updated/Removed) for the HTTP /api/state
// endpoint which still returns a full snapshot.
type Snapshot struct {
	Type     string `json:"type"`
	SectorID int64  `json:"sectorID"`
	Tick     uint64 `json:"tick"`
	// TimeScale is the sector's time-dilation factor (phase 7.2): 1.0 = real
	// time, < 1.0 = slowed under overload. The SPA shows a dilation indicator
	// when below 1.0.
	TimeScale float64        `json:"timeScale,omitempty"`
	Ships     []Ship         `json:"ships,omitempty"`
	Statics   *SectorStatics `json:"statics,omitempty"`
	Added     []Ship         `json:"added,omitempty"`
	Updated   []Ship         `json:"updated,omitempty"`
	Removed   []int64        `json:"removed,omitempty"`
	// LaserEffects holds one-frame laser shots that fired this tick.
	// The SPA draws each beam for one frame and discards it on the
	// next snapshot. Absent or empty between ticks. Phase 4.2.
	LaserEffects []LaserBeam `json:"laserEffects,omitempty"`

	// MissilesAdded / MissilesUpdated / MissilesRemoved is the missile
	// diff against the subscriber's previous tick view. Phase 4.3.
	MissilesAdded   []Missile `json:"missilesAdded,omitempty"`
	MissilesUpdated []Missile `json:"missilesUpdated,omitempty"`
	MissilesRemoved []int64   `json:"missilesRemoved,omitempty"`

	// MissileImpacts holds one-frame missile events (hit / expired)
	// that fired this tick.
	MissileImpacts []MissileImpact `json:"missileImpacts,omitempty"`

	// DronesAdded / DronesUpdated / DronesRemoved is the drone diff
	// against the subscriber's previous tick view. Phase 4.4.
	DronesAdded   []Drone `json:"dronesAdded,omitempty"`
	DronesUpdated []Drone `json:"dronesUpdated,omitempty"`
	DronesRemoved []int64 `json:"dronesRemoved,omitempty"`

	// DroneImpacts holds one-frame drone events (shot / death / expire)
	// that fired this tick.
	DroneImpacts []DroneImpact `json:"droneImpacts,omitempty"`

	// ContainersAdded / ContainersRemoved is the loot-container diff
	// against the subscriber's previous tick view (immutable, so no
	// "updated"). Phase 4.6.
	ContainersAdded   []Container `json:"containersAdded,omitempty"`
	ContainersRemoved []int64     `json:"containersRemoved,omitempty"`

	// StaticsUpdated carries the new HP/Shield of statics that took damage
	// or recharged this tick; StaticsRemoved is the ref list of statics
	// destroyed this tick. The SPA patches the combat state of objects it
	// got once via the StaticsMessage. Phase 6.2b.
	StaticsUpdated []DestructibleStatic `json:"staticsUpdated,omitempty"`
	StaticsRemoved []EntityRef          `json:"staticsRemoved,omitempty"`

	// StaticsAdded carries the full static objects that just entered the
	// player's big-radar window (phase 10.20 L2). Same shape as the welcome
	// StaticsMessage; the SPA merges these into its statics map. Statics that
	// left the window arrive in StaticsRemoved.
	StaticsAdded *SectorStatics `json:"staticsAdded,omitempty"`
}

// DestructibleStatic is the live combat state of one static object on the
// wire (phase 6.2b). Ref identifies which static (kind+id) to patch.
type DestructibleStatic struct {
	Ref       EntityRef `json:"ref"`
	HP        int       `json:"hp"`
	Shield    int       `json:"shield"`
	MaxShield int       `json:"maxShield"`
}

// LaserBeam mirrors combat.LaserBeam on the wire.
type LaserBeam struct {
	AttackerShipID int64     `json:"attacker"`
	Target         EntityRef `json:"target"`
	FromX          float64   `json:"fromX"`
	FromY          float64   `json:"fromY"`
	ToX            float64   `json:"toX"`
	ToY            float64   `json:"toY"`
	Damage         int       `json:"damage"`
	Killed         bool      `json:"killed,omitempty"`
}

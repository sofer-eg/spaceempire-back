package sector

import "time"

// Config tunes a single Worker. PoolConfig embeds this so every worker in a
// Pool gets the same per-tick parameters.
type Config struct {
	TickInterval     time.Duration
	SnapshotInterval time.Duration
	InboxCapacity    int
	// GateRange is the radius in world units a ship must be within of a
	// gate's exit position before JumpCommand is accepted. Larger than the
	// per-tick movement step so the player has a forgiving window.
	GateRange float64
	// DockRange is the radius in world units a ship must be within of a
	// static dockable object before DockCommand succeeds. Phase 3.12 dropped
	// the unconditional tick-driven auto-docking; phase 10.3.10 brought it
	// back gated on the up_docking module (tryAutoDock) — ships without it are
	// still player-issued only. Smaller than GateRange because manual docking
	// is a deliberate act, so a tight tolerance prevents stray clicks.
	DockRange float64
	// AOIRadius is the per-player Area of Interest radius in world units.
	// Each subscription only receives patches for ships within this radius
	// of the player's own ship (or of the world origin while the player has
	// no ship in the sector). Default mirrors phase-3.5 balance.yaml — to be
	// reconciled with config.tp.php during balance port.
	AOIRadius float64
	// ShutdownTimeout bounds the graceful-shutdown flush (Worker.flushAll),
	// which persists every ship's live position when Run's context is
	// cancelled. Phase 3.19 (approach B) stops writing position periodically,
	// so this flush is the only thing that ends a clean run with fresh
	// coordinates. Wired from cfg.Server.ShutdownTimeout.
	ShutdownTimeout time.Duration
	// ContainerTTL is how long a loot container (dropped by a ship death,
	// phase 4.6) survives before the tick sweeps it. Default 600 s.
	ContainerTTL time.Duration
	// PickupRange is the radius in world units a ship must be within of a
	// container before PickupContainerCommand succeeds. Looser than
	// DockRange — a container is not a dockable object ("достаточно
	// близко"). Default 30.
	PickupRange float64
	// StealthDetectRange is how close a hostile must get before a cloaked ship
	// (up_hide, phase 10.20 L4) surfaces in their AOI. Smaller than any radar so
	// stealth is meaningful but not absolute. Default 400.
	StealthDetectRange float64
	// RadarBigMultiplier scales the personal radar into the big-object radar
	// (phase 10.20 L2): large statics (stations/shipyards/TS/pirbases/towers)
	// are visible within RadarRange × this. Calibrated down from the original ×5
	// so statics still enter/leave by movement within a sector. Default 2.5.
	RadarBigMultiplier float64
	// SatelliteRevealRadius is the AOI radius every subscriber gets while at
	// least one live navigation satellite (phase 10.15) is present in the
	// sector. Default 10000 — twice the ±5000 sector half-extent, so it covers
	// the whole sector from any interior point (both the ship AOI window and,
	// via RadarBigMultiplier, the big-object static window).
	SatelliteRevealRadius float64
	// MineRange is how close a player ship must be to an asteroid to keep
	// sustained mining (phase 10.3.6). Matches the NPC miner's MineRange so
	// players and NPC drill within the same window. Default 12.
	MineRange float64
	// MineRate is the ore a player ship drills per tick (phase 10.3.6). Matches
	// the NPC miner's DrillRate=5 so the world's mining magnitude is uniform.
	// Default 5.
	MineRate int64
	// MineEnergyCost is the per-tick "action" energy a player ship spends to
	// drill (phase 10.3.1/10.3.6), resolved from the up_drill catalog row at
	// build time. Below this the ship cannot drill this tick. 0 disables the
	// gate (unit tests / a catalog with no up_drill energy_usage).
	MineEnergyCost int
	// ExternalDockTurns is how many ticks the up_exdocking external-docking
	// process runs before it attaches (phase 10.3.23, port of the SP
	// dock_suspension_time = 1). Default 1.
	ExternalDockTurns int
}

func (c Config) withDefaults() Config {
	if c.TickInterval <= 0 {
		c.TickInterval = 3 * time.Second
	}
	if c.SnapshotInterval <= 0 {
		c.SnapshotInterval = 5 * time.Second
	}
	if c.InboxCapacity <= 0 {
		c.InboxCapacity = 256
	}
	if c.GateRange <= 0 {
		c.GateRange = 50
	}
	if c.DockRange <= 0 {
		c.DockRange = 3
	}
	if c.AOIRadius <= 0 {
		c.AOIRadius = 5000
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
	if c.ContainerTTL <= 0 {
		c.ContainerTTL = 600 * time.Second
	}
	if c.PickupRange <= 0 {
		c.PickupRange = 30
	}
	if c.StealthDetectRange <= 0 {
		c.StealthDetectRange = 400
	}
	if c.RadarBigMultiplier <= 0 {
		c.RadarBigMultiplier = 2.5
	}
	if c.SatelliteRevealRadius <= 0 {
		c.SatelliteRevealRadius = 10000
	}
	if c.MineRange <= 0 {
		c.MineRange = 12
	}
	if c.MineRate <= 0 {
		c.MineRate = 5
	}
	if c.ExternalDockTurns <= 0 {
		c.ExternalDockTurns = 1
	}
	return c
}

// PoolConfig configures how many workers a Pool spawns and the per-worker
// tick parameters they all share.
type PoolConfig struct {
	WorkersCount int
	Worker       Config
}

func (p PoolConfig) withDefaults() PoolConfig {
	if p.WorkersCount <= 0 {
		p.WorkersCount = 1
	}
	p.Worker = p.Worker.withDefaults()
	return p
}

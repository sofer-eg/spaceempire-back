package sector

import (
	"time"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

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
	// AOIRadius is the radar-less fallback view radius in world units: the
	// per-player Area of Interest radius used when the player's own ship has no
	// class radar (legacy/spacesuit, RadarRange<=0) or has no ship in the sector
	// (observer, centred on the world origin). It also sizes the spatial grid
	// cell. Default 400 (TASK-123), a sane radar below the sector half-extent
	// (~1000) so the fallback stays a real visibility limit — it must NOT reveal
	// the whole sector (TASK-117). Ships with a class radar use their own
	// RadarRange, not this.
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
	// SatelliteRevealRadius is the AOI radius every subscriber gets while at
	// least one live navigation satellite (phase 10.15) is present in the
	// sector. Default 10000 — twice the ±5000 sector half-extent, so it covers
	// the whole sector from any interior point (widening the personal-radar
	// window so far ships and radar-gated statics — towers/satellites — become
	// visible; stations/shipyards/TS/pirbases and asteroids are always visible).
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
	// TransporterRange is how close (world units) the source ship must be to the
	// up_transporter ship for a cargo teleport (phase 10.3.18). Default 250 — a
	// no-dock reach far longer than PickupRange.
	TransporterRange float64
	// TransporterEnergyCost is the "action" energy a cargo teleport spends
	// (phase 10.3.1/10.3.18), resolved from the up_transporter catalog row at
	// build time. Below this the teleport is rejected. 0 disables the gate.
	TransporterEnergyCost int
	// ExternalDockTurns is how many ticks the up_exdocking external-docking
	// process runs before it attaches (phase 10.3.23, port of the SP
	// dock_suspension_time = 1). Default 1.
	ExternalDockTurns int
	// Knock tunes the module-knockoff mechanic (TASK-100.3.9.1, port of SP
	// DestroyModule): a hit on a ship whose shield is down can strip an installed
	// module off for good. The scalars come from configs/capture.yaml; Positions
	// is the Type→slot lookup the app fills from the equipment catalog. Zero
	// scalars fall back to combat.DefaultKnockConfig in withDefaults.
	Knock combat.KnockConfig
	// HackRange is how close (world units) the hacker ship must be to a trade
	// station to raid it with up_hack (TASK-100.3.9.3, SP UseHack), from
	// capture.yaml. Default 50.
	HackRange float64
	// CaptureChance / KhaakCaptureChance are the ship-capture roll thresholds on a
	// 0..1000 scale (TASK-100.3.9.4, SP DoCapture): a capture succeeds when
	// rng.Float64()*1000 > threshold. CaptureChance (819, ~18%) is the generic
	// case; KhaakCaptureChance (876, ~12%) applies when the target is Kha'ak (race
	// 8). From capture.yaml; NFR-004 keeps them out of code.
	CaptureChance      float64
	KhaakCaptureChance float64
	// CaptureRange is how close (world units) the attacker must be to the target
	// ship to attempt a capture (SP DoCapture, √2500 = 50). Default 50.
	CaptureRange float64
	// JumpDriveCooldownL1 / JumpDriveCooldownL2 are the real-time cooldowns a
	// seamless jump drive (up_jump_drive) enforces between hops (TASK-100.3.7,
	// faithful SP DoJump: level 1 = 3600 s, level 2 = 1800 s). The command reads
	// them by the ship's module level so an upgraded drive recharges twice as
	// fast. Held in config (not code) so tests can inject tiny values. Defaults
	// 60 min / 30 min.
	JumpDriveCooldownL1 time.Duration
	JumpDriveCooldownL2 time.Duration
	// JumpDriveForbiddenSectors lists the sectors a ship may NOT jump OUT of with
	// a jump drive (TASK-100.3.7, port of SP DoJump's `object_sector = 215 or
	// 203` gate). Empty by default — the StarWind-specific ids are NOT hardcoded;
	// a deployment wires its own list if it wants no-jump zones.
	JumpDriveForbiddenSectors []domain.SectorID
	// AntijumpRange is how close (world units) a powered up_antijump ship must be
	// to a would-be jumper to jam its seamless jump (TASK-100.3.8, SP DoJump's
	// hyper-interference gate). The original SP tested a box |dx|<640 && |dy|<640;
	// we port that as a circular Euclidean radius for uniformity with
	// MineRange/TransporterRange/CaptureRange (all `Pos.Sub().Length()`). Default 640.
	AntijumpRange float64
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
		// A sane radar below the sector half-extent (~1000), so the radar-less
		// fallback (spacesuit/observer) does not reveal the whole sector
		// (TASK-117, re-calibrated TASK-123). Mirrors the balance radarDefault.
		c.AOIRadius = 400
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
	if c.SatelliteRevealRadius <= 0 {
		c.SatelliteRevealRadius = 10000
	}
	if c.MineRange <= 0 {
		c.MineRange = 12
	}
	if c.MineRate <= 0 {
		c.MineRate = 5
	}
	if c.TransporterRange <= 0 {
		c.TransporterRange = 250
	}
	if c.ExternalDockTurns <= 0 {
		c.ExternalDockTurns = 1
	}
	if c.Knock.CriticalShieldCharge <= 0 {
		d := combat.DefaultKnockConfig()
		c.Knock.CriticalShieldCharge = d.CriticalShieldCharge
		c.Knock.CriticalHullIntegrity = d.CriticalHullIntegrity
		c.Knock.ExternalBase = d.ExternalBase
		c.Knock.InternalBase = d.InternalBase
	}
	if c.HackRange <= 0 {
		c.HackRange = 50
	}
	if c.CaptureChance <= 0 {
		c.CaptureChance = 819
	}
	if c.KhaakCaptureChance <= 0 {
		c.KhaakCaptureChance = 876
	}
	if c.CaptureRange <= 0 {
		c.CaptureRange = 50
	}
	if c.JumpDriveCooldownL1 <= 0 {
		c.JumpDriveCooldownL1 = 60 * time.Minute
	}
	if c.JumpDriveCooldownL2 <= 0 {
		c.JumpDriveCooldownL2 = 30 * time.Minute
	}
	if c.AntijumpRange <= 0 {
		c.AntijumpRange = 640
	}
	return c
}

// jumpDriveCooldown returns the real-time cooldown for a jump drive of the given
// install level (TASK-100.3.7): level 2 recharges in half the time of level 1.
func (c Config) jumpDriveCooldown(level int) time.Duration {
	if level >= 2 {
		return c.JumpDriveCooldownL2
	}
	return c.JumpDriveCooldownL1
}

// sectorForbidden reports whether ships may not jump OUT of the given sector
// with a jump drive (TASK-100.3.7). Empty list ⇒ every sector is jumpable.
func (c Config) sectorForbidden(id domain.SectorID) bool {
	for _, f := range c.JumpDriveForbiddenSectors {
		if f == id {
			return true
		}
	}
	return false
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

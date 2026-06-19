package domain

type Ship struct {
	ID       ShipID
	PlayerID PlayerID
	// ShipClassID is the ct_ship_classes blueprint the ship was built from
	// (phase 10.13). The combat ТТХ are copied into the fields below at spawn;
	// this id is kept so the snapshot can resolve a hull-shape category
	// (balance.ShipClass.Category) without re-deriving it from MaxSpeed. Set at
	// Create, immutable. 0 = unknown / spacesuit / legacy ship.
	ShipClassID ShipClassID
	// Race is the faction the ship belongs to (phase 9.1): 0 = neutral
	// (players), 1–8 = NPC races. Drives Race-AI hostility via the
	// race.DefaultStanding matrix (8.13). Set at Create, immutable after.
	Race RaceID
	// Name is the ship's display name (StarWind ships.name, varchar(32)). The
	// starter ship is named after its race's M5 model at spawn (Argon ->
	// "Разведчик"); empty for NPC/legacy ships. Set at Create, persisted as a
	// plain column — there is no in-game rename yet (phase 10.10).
	Name     string
	SectorID SectorID
	Pos      Vec2
	Vel      Vec2
	// MaxSpeed is the upper bound on |Vel| the ship can reach under its
	// own thrust, in world units **per tick**. The original StarWind SP
	// `TO_ShipMovement` integrates pos += vel every tick (no dt scaling),
	// so every kinematic quantity here is per-tick too — calibrated to
	// the sector's TickInterval, not to wall-clock seconds.
	MaxSpeed float64
	// Acceleration is the per-tick delta-|Vel| applied by the engine
	// when thrusting (and, negated, the brake when decelerating).
	Acceleration float64
	// TurnRate is the maximum heading change per tick, in radians. Above
	// π the ship turns instantly (matches the SP's `grad_speed > 180`
	// shortcut); below π it rotates Direction toward the desired heading
	// via a rotation matrix every tick.
	TurnRate float64
	// Direction is the unit vector pointing along the ship's nose. Stored
	// (instead of an angle) so the integrator can rotate via a 2×2
	// matrix without ever calling atan2 — same shape as the SP's
	// `direction_x, direction_y`. (1, 0) is the spawn default.
	Direction Vec2
	// Target is the next waypoint inside the current sector. Set either by
	// MoveCommand (manual point-and-click) or by the autopilot resolver
	// (chasing the next gate / the final position).
	Target *Vec2
	// FinalTarget, when non-nil, drives the autopilot: every tick the
	// resolver computes Target from FinalTarget + the path router, and the
	// ship jumps through gates automatically until it reaches the target
	// sector. nil disables the autopilot — only Target is consulted.
	FinalTarget *Course
	HP          int
	// MaxHP is the upper bound on HP — ApplyDamage cannot push HP above
	// it (no negative damage / healing on this phase). Set at Create
	// time from the ship class, never updated afterwards.
	MaxHP  int
	Shield int
	// MaxShield is the upper bound on Shield. When MaxShield==0 the
	// ship has no shield module — ChargeShield is a no-op and any
	// damage goes straight into HP. Set at Create time.
	MaxShield int
	// ShieldRecharge is the per-tick delta added to Shield by
	// ChargeShield, calibrated to TickInterval (same convention as
	// MaxSpeed/Acceleration). Original SP `TO_ShipShieldCharge` adds
	// `ships.shield_charge` (int) per call, so this stays int as well.
	ShieldRecharge int
	// Energy/MaxEnergy/EnergyRecharge are the per-tick energy pool
	// used by lasers and (later) other active modules. Same int
	// per-tick convention as Shield. MaxEnergy==0 = no powerplant —
	// FireLaser is a no-op for such a ship.
	Energy         int
	MaxEnergy      int
	EnergyRecharge int
	// EnergyDelta is the net per-tick energy change contributed by installed
	// equipment (phase 10.3.1): Σ energy_usage of "reverse" modules (generators,
	// +) minus Σ energy_usage of "always" modules (constant consumers, −). It is
	// folded with EnergyRecharge by combat.ChargeEnergy each tick. Cached at
	// install/uninstall via balance.Equipments.EnergyDelta (like the effective
	// stats) and persisted as a derived column; 0 for ships with no equipment.
	EnergyDelta int
	// LaserDamage/LaserRange/LaserEnergyCost describe the ship's
	// laser turret as a single fixed weapon. Per-tick damage on hit.
	// LaserDamage==0 = no laser equipped — FireLaser early-returns.
	// On the future class/upgrade catalog these will come from balance
	// data; until then every spawn gets the spawner defaults.
	LaserDamage     int
	LaserRange      float64
	LaserEnergyCost int
	// RadarRange is the personal small-radar radius (phase 10.20): the AOI
	// window the player's subscription uses to see ships/missiles/drones/
	// containers around this ship. Sourced from the ship class
	// (balance.ShipClass.Radar, defaulted by category) at spawn and folded with
	// up_scanner via the equipment-effect pipeline (L3). 0 = legacy/spacesuit —
	// the subscription then falls back to cfg.AOIRadius. Large objects use
	// RadarRange × cfg.RadarBigMultiplier (L2). Persisted as ships.radar_range.
	RadarRange float64
	// CargoBay is the ship's cargo hold capacity in space units. Sourced from the
	// ship class (balance.ShipClass.CargoBay) at spawn — before phase 10.3.17 the
	// ships.cargobay column was left at its 100-unit default, ignoring the class —
	// and folded with up_cargobay via the equipment-effect pipeline (phase
	// 10.3.16). Persisted as ships.cargobay; read back as cargo Capacity.
	CargoBay float64
	// IsHidden marks a cloaked ship (phase 10.20 L4, ported from up_hide): it is
	// cached "has an up_hide module fitted", refreshed whenever the equipment
	// list changes (install/uninstall, cold-start, add). The sector worker
	// excludes a cloaked ship from other players' AOI patches — except own/
	// allied subscribers and close hostiles — and treats it as surfaced (still
	// visible) while it has a live AttackTarget. RAM-only, not persisted.
	IsHidden bool
	// MissileJustFired is a one-tick stealth reveal flag (phase 10.20a): set in
	// LaunchMissileCommand.apply when the ship is cloaked, cleared at the end of
	// the same tick (after the snapshot). hideStealthed treats it like AttackTarget
	// — the ship is visible for that snapshot. RAM-only, not persisted.
	MissileJustFired bool
	// AttackTarget, when non-nil, is the entity the laser tick is
	// shooting at. Cleared by sector worker when the target dies,
	// leaves the sector, or on explicit CeaseFireCommand. Independent
	// from CurrentTargetRef (which is the navigation target). Phase
	// 4.2 only supports EntityKindShip targets; the worker enforces.
	AttackTarget *EntityRef
	// Docked, when non-nil, marks the ship as parked inside a dockable
	// object: a static (station/shipyard/trade station/pirbase) or — since
	// phase 10.3.24 — another (moving) host ship (Docked.Kind ==
	// EntityKindShip), carried along in the host's hangar. A docked ship does
	// not move under its own thrust and ignores Target/FinalTarget; a
	// ship-docked one is snapped to the host's position each tick
	// (carryDockedShips). Cleared by undock.
	Docked *EntityRef
	// ExternalDock, when non-nil, is the in-progress external-docking process
	// (phase 10.3.23, up_exdocking): the ship is engaging external clamps to
	// attach to a moving host ship its hangar cannot hold. TurnsLeft counts
	// down each tick; at 0 the ship attaches to Target bypassing hangar
	// capacity (executeExternalAttach). RAM-only transient intent, like
	// MiningTarget — not persisted (the original up_status counter lasts ~1
	// tick). Cleared on completion, cancellation, or a fresh move/course.
	ExternalDock *ExternalDock
	// MiningTarget, when non-nil, is the asteroid the player is sustained-
	// mining (phase 10.3.6, gated on up_drill). Each tick the worker holds
	// the ship on station and drills it while in range with enough energy;
	// the ore deposit depends on the up_drill level (L1 -> loot container,
	// L2 -> hold with container fallback). Set by MineCommand, cleared by
	// CeaseFireCommand, by a fresh MoveCommand, when the asteroid is depleted,
	// and dropped on a gate jump (the asteroid lives in the source sector).
	// RAM-only transient intent, like AttackTarget — not persisted.
	MiningTarget *AsteroidID
	// CurrentTargetRef, when non-nil, marks the entity the player explicitly
	// "is flying to" so the SPA can paint a persistent highlight on it
	// (distinct from the hover-only highlight). Set by MoveCommand.TargetRef
	// or Course.Approach; cleared on dock/undock and on plain arrival (when
	// FinalTarget.Approach is nil). In approach mode it stays through parking
	// so the player still sees what they're parked next to. Not persisted —
	// derived from FinalTarget.Approach at cold-start.
	CurrentTargetRef *EntityRef
	// Passengers is how many passengers the ship is carrying. NPC passenger
	// TS board a random number on departure and drop them to 0 on arrival
	// (phase 5.5); on a kill a non-zero count spills "slaves" loot (5.6).
	// Persisted by the immediate Save (board/drop are discrete events).
	Passengers int
	// Equipment is the list of ct_updates modules installed on the ship
	// (phase 10.14). Stat modules (up_engine, up_shield, …) fold into the
	// MaxSpeed/MaxShield/Energy/Laser fields above at install time via
	// balance.ApplyEquipmentEffects; capability modules (up_drill, up_scanner,
	// …) are kept here for the subsystem that consumes them. Persisted as a
	// JSONB column; empty for NPC/legacy/spacesuit ships. See
	// back/docs/specs/equipment_effects.md.
	Equipment []InstalledEquipment
	// IsSpacesuit marks the weak pilot "ship" a player drops into when their
	// real ship is destroyed (phase 10.1, ported from the StarWind 'Скафандр').
	// Set at Create, immutable. Destroying a spacesuit respawns the player a
	// fresh ship at the home shipyard instead of another suit. Real player
	// ships and NPC ships are false.
	IsSpacesuit bool
	// LastAttacker is the player who most recently dealt damage to this ship
	// (0 = none). Set by every weapon damage site (laser/missile/drone/tower)
	// to the attacking player; read by the kill sweep to attribute the kill
	// for bounty payout (phase 6.3). RAM-only combat state, like AttackTarget.
	LastAttacker PlayerID
	// IsOpen marks a ship that another player may board as a passenger (phase
	// 10.23). Player ships default closed; NPC ships are spawned open. Own
	// ships are always boardable regardless of this flag; this gates boarding
	// of OTHER players' ships. Persisted (toggled by the ship-access command).
	IsOpen bool
	// PassengerPlayers lists the players currently riding this ship as
	// passengers (phase 10.23). Source of truth is players.passenger_of_ship_id;
	// this is the RAM mirror the worker uses to fan out PlayerHandoffEvent on a
	// gate jump and to eject passengers on death. Carried across handoff in the
	// JumpEvent; rebuilt at cold-start from the players table. Not a DB column.
	PassengerPlayers []PlayerID
}

// ExternalDock is the state of an in-progress up_exdocking external-docking
// process (phase 10.3.23): the host ship being attached to and how many ticks
// remain before the clamps engage. The worker replaces this pointer each tick
// (never mutates TurnsLeft in place) so a published snapshot that aliases it
// never sees a concurrent write.
type ExternalDock struct {
	Target    EntityRef
	TurnsLeft int
}

// InstalledEquipment is one ct_updates module fitted on a ship (phase 10.14).
// EquipmentID pins the exact catalog row (the per-class price tier); Type is
// denormalized so the sector worker and cold-start load can fold stat effects
// without the catalog in hand. Level is the install level (>=1).
type InstalledEquipment struct {
	EquipmentID EquipmentID
	Type        string
	Level       int
}

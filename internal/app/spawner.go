package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	shipsrepo "spaceempire/back/internal/persistence/ships"
	"spaceempire/back/internal/sector"
)

// MissileGoodsType identifies the cargo row consumed per missile launch.
// Seeded by migration 0017_missile_goods.sql.
const MissileGoodsType domain.GoodsTypeID = 50

// ShipSpawnerConfig knobs the spawner exposes to app wiring.
type ShipSpawnerConfig struct {
	SectorID         domain.SectorID
	SpawnHalfX       float64       // ±X around origin where ships spawn
	SpawnHalfY       float64       // ±Y around origin where ships spawn
	StartMaxSpeed    float64       // starter ship MaxSpeed (world units/s)
	StartAccel       float64       // starter ship Acceleration (units/s²)
	StartTurnRate    float64       // starter ship TurnRate (rad/s)
	StartHP          int           // starter HP (current = max at spawn)
	StartShld        int           // starter Shield (current = max at spawn)
	StartShldCharge  int           // starter ShieldRecharge per tick (phase 4.1)
	StartEnergy      int           // starter Energy (current = max at spawn, phase 4.2)
	StartEnergyChrg  int           // starter EnergyRecharge per tick (phase 4.2)
	StartLaserDamage int           // starter LaserDamage per tick on hit (phase 4.2)
	StartLaserRange  float64       // starter LaserRange (world units, phase 4.2)
	StartLaserECost  int           // starter LaserEnergyCost per shot (phase 4.2)
	StartMissiles    int64         // initial missile cargo (phase 4.3)
	AckTimeout       time.Duration // how long to wait for the worker to accept AddShipCommand
	// Spacesuit (phase 10.1): the weak pilot "ship" a player drops into when
	// their real ship is destroyed. Original StarWind hull=1/laser=5/shield=0;
	// HP is softened for the real-time engine (configurable). No shield/cargo.
	SpacesuitHP          int     // spacesuit hull (default 30; original 1)
	SpacesuitLaserDamage int     // spacesuit laser (default 5, the original)
	SpacesuitMaxSpeed    float64 // spacesuit speed (default = StartMaxSpeed, to flee)
	// TradeInRatio is the fraction of a ship's class base_price refunded when
	// the player sells it at a shipyard (phase 10.14a trade-in). Default 0.5.
	TradeInRatio float64
}

func (c ShipSpawnerConfig) withDefaults() ShipSpawnerConfig {
	if c.SpawnHalfX <= 0 {
		c.SpawnHalfX = 100
	}
	if c.SpawnHalfY <= 0 {
		c.SpawnHalfY = 100
	}
	if c.StartMaxSpeed <= 0 {
		c.StartMaxSpeed = 20
	}
	if c.StartAccel <= 0 {
		c.StartAccel = 10
	}
	if c.StartTurnRate <= 0 {
		// π/4 rad/tick ≈ 45°/tick. At TickInterval=3s that is 15°/sec,
		// gentle enough that target changes look like "the submarine
		// rolls into a turn" rather than a teleport.
		c.StartTurnRate = math.Pi / 4
	}
	if c.StartHP <= 0 {
		c.StartHP = 100
	}
	if c.StartShld <= 0 {
		c.StartShld = 100
	}
	if c.StartShldCharge <= 0 {
		c.StartShldCharge = 1
	}
	if c.StartEnergy <= 0 {
		c.StartEnergy = 100
	}
	if c.StartEnergyChrg <= 0 {
		c.StartEnergyChrg = 2
	}
	if c.StartLaserDamage <= 0 {
		c.StartLaserDamage = 10
	}
	if c.StartLaserRange <= 0 {
		c.StartLaserRange = 400
	}
	if c.StartLaserECost <= 0 {
		c.StartLaserECost = 5
	}
	if c.StartMissiles <= 0 {
		// 5 starter missiles — enough to fight a small skirmish and notice
		// the cargo cost, not so many a new player can spam.
		c.StartMissiles = 5
	}
	if c.AckTimeout <= 0 {
		c.AckTimeout = time.Second
	}
	if c.SpacesuitHP <= 0 {
		c.SpacesuitHP = 30
	}
	if c.SpacesuitLaserDamage <= 0 {
		c.SpacesuitLaserDamage = 5
	}
	if c.SpacesuitMaxSpeed <= 0 {
		c.SpacesuitMaxSpeed = c.StartMaxSpeed
	}
	if c.TradeInRatio <= 0 {
		c.TradeInRatio = 0.5
	}
	return c
}

// playerRaceReader reads the race a player picked at registration. The real
// implementation is *auth.Repository; nil disables race lookup (the starter
// ship then spawns neutral in the config sector, the pre-10.10 behaviour
// used by minimal deployments and unit tests).
type playerRaceReader interface {
	PlayerRace(ctx context.Context, playerID domain.PlayerID) (domain.RaceID, error)
}

// homeShipyard is a race's home NPC shipyard — where its players spawn their
// starter ship (phase 10.10). Built once at startup from the loaded statics.
type homeShipyard struct {
	Sector domain.SectorID
	Pos    domain.Vec2
}

// buildHomeShipyards maps each race to its home NPC shipyard from the loaded
// statics (phase 10.10). When a race has several shipyards it prefers the one
// in the lowest sector id — which, for the playable races, is the original
// races.CentralSector (Argon→1, Boron→5, Paranid→9, Split→13, Teladi→17).
// Race 0 (unowned) shipyards are ignored.
func buildHomeShipyards(statics map[domain.SectorID]domain.SectorStatics) map[domain.RaceID]homeShipyard {
	out := make(map[domain.RaceID]homeShipyard)
	for _, st := range statics {
		for _, sy := range st.Shipyards {
			race := domain.RaceID(sy.Race)
			if race == 0 {
				continue
			}
			if cur, ok := out[race]; !ok || sy.SectorID < cur.Sector {
				out[race] = homeShipyard{Sector: sy.SectorID, Pos: sy.Pos}
			}
		}
	}
	return out
}

// shipSpawner is the auth.ShipSpawner implementation: it persists a fresh
// ship row for the player and tells the sector pool to mirror it in RAM
// so existing subscribers see the new ship on the next tick.
type shipSpawner struct {
	repo    *shipsrepo.Repository
	cargo   *cargorepo.Repository
	pool    *sector.Pool
	cfg     ShipSpawnerConfig
	rng     *rand.Rand
	players playerRaceReader
	// homeYards maps a race to its home shipyard's sector+position. A player
	// of that race spawns their starter ship there (phase 10.10). Missing
	// race → fall back to cfg.SectorID + a random offset.
	homeYards map[domain.RaceID]homeShipyard
	// classes is the ship-class catalog (8.14) used to name the starter ship
	// after its race's M5 model. nil → no name (client falls back to #id).
	classes *balance.ShipClasses
}

// newShipSpawner wires the dependencies. cargoRepo is allowed to be nil
// when starter cargo is disabled (tests, deployments without the cargo
// table) — `SpawnFor` skips the cargo step in that case. players, homeYards
// and classes (all phase 10.10) may be nil/empty — the spawner then falls
// back to the pre-10.10 neutral-ship-in-config-sector behaviour.
func newShipSpawner(repo *shipsrepo.Repository, cargoRepo *cargorepo.Repository, pool *sector.Pool, cfg ShipSpawnerConfig, players playerRaceReader, homeYards map[domain.RaceID]homeShipyard, classes *balance.ShipClasses) *shipSpawner {
	return &shipSpawner{
		repo:  repo,
		cargo: cargoRepo,
		pool:  pool,
		cfg:   cfg.withDefaults(),
		// PCG seeded from time — fine for spawn positioning; not used for crypto.
		rng:       rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xdeadbeef)),
		players:   players,
		homeYards: homeYards,
		classes:   classes,
	}
}

// SpawnFor creates a starter ship for the player at their race's home
// shipyard (phase 10.10): the ship gets the player's race and is named after
// that race's M5 model. Used at registration. When the race is unset or has
// no home shipyard, it falls back to the config sector at a random point.
func (s *shipSpawner) SpawnFor(ctx context.Context, playerID domain.PlayerID) error {
	race, err := s.playerRace(ctx, playerID)
	if err != nil {
		return err
	}
	sectorID, pos := s.homeSpawn(race)
	return s.spawnStarter(ctx, playerID, race, s.starterName(race), sectorID, pos)
}

// SpawnStarterAt creates a starter ship for the player at a specific sector and
// position. Used when a spacesuit pilot gets a new ship at a shipyard (phase
// 10.2) — spawned at the suit's docked position so the player keeps watching
// the same sector (no handoff). The ship still gets the player's race / M5
// name (phase 10.10).
func (s *shipSpawner) SpawnStarterAt(ctx context.Context, playerID domain.PlayerID, sectorID domain.SectorID, pos domain.Vec2) error {
	race, err := s.playerRace(ctx, playerID)
	if err != nil {
		return err
	}
	return s.spawnStarter(ctx, playerID, race, s.starterName(race), sectorID, pos)
}

// playerRace reads the player's chosen race. With no race reader wired (tests
// / minimal deployments) it returns 0 (neutral) and no error.
func (s *shipSpawner) playerRace(ctx context.Context, playerID domain.PlayerID) (domain.RaceID, error) {
	if s.players == nil {
		return 0, nil
	}
	race, err := s.players.PlayerRace(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("read player race: %w", err)
	}
	return race, nil
}

// homeSpawn returns where a player of the given race spawns their starter
// ship: just off their race's home shipyard, or — when the race is unset or
// has no home yard — a random point in the config sector (pre-10.10 default).
func (s *shipSpawner) homeSpawn(race domain.RaceID) (domain.SectorID, domain.Vec2) {
	if yard, ok := s.homeYards[race]; ok {
		// Spawn just off the shipyard in open space (not docked) so the
		// player flies immediately. Small ±offset avoids stacking pilots.
		offset := domain.Vec2{
			X: (s.rng.Float64()*2 - 1) * s.cfg.SpawnHalfX,
			Y: (s.rng.Float64()*2 - 1) * s.cfg.SpawnHalfY,
		}
		return yard.Sector, domain.Vec2{X: yard.Pos.X + offset.X, Y: yard.Pos.Y + offset.Y}
	}
	return s.cfg.SectorID, domain.Vec2{
		X: (s.rng.Float64()*2 - 1) * s.cfg.SpawnHalfX,
		Y: (s.rng.Float64()*2 - 1) * s.cfg.SpawnHalfY,
	}
}

// starterName returns the name of the race's M5 model (e.g. Argon →
// "Разведчик"), or "" when the catalog is absent or the race has no scout.
func (s *shipSpawner) starterName(race domain.RaceID) string {
	if s.classes == nil {
		return ""
	}
	if sc, ok := s.classes.ScoutForRace(int(race)); ok {
		return sc.Name
	}
	return ""
}

// starterClass returns the M5 catalog entry for a race, used to set combat
// stats on the starter ship. Returns false when the catalog is absent or the
// race has no scout class.
func (s *shipSpawner) starterClass(race domain.RaceID) (balance.ShipClass, bool) {
	if s.classes == nil {
		return balance.ShipClass{}, false
	}
	return s.classes.ScoutForRace(int(race))
}

// baseShipStats is the equipment-free baseline of the stat fields equipment
// can modify (phase 10.14): the class supplies speed/accel/shield/laser, while
// the energy pools come from spawn config (the ship-class catalog carries no
// energy). Used both to build a freshly purchased ship and to recompute
// effective stats when a module is installed/removed — keeping a single source
// for the laser divisor/floor so a bare bought ship and its later recompute
// agree. Mirrors the per-class branch of spawnStarter.
func baseShipStats(cls balance.ShipClass, cfg ShipSpawnerConfig) balance.ShipStats {
	laser := cls.Laser / warshipLaserDivisor
	if laser < cfg.StartLaserDamage {
		laser = cfg.StartLaserDamage
	}
	return balance.ShipStats{
		MaxSpeed:       cls.Speed,
		Acceleration:   cls.Acceleration,
		TurnRate:       cfg.StartTurnRate, // phase 10.3.15: base turn rate, widened by up_rudder
		MaxShield:      cls.Shield,
		ShieldRecharge: cls.ShieldCharge,
		MaxEnergy:      cfg.StartEnergy,
		EnergyRecharge: cfg.StartEnergyChrg,
		LaserDamage:    laser,
		RadarRange:     float64(cls.Radar),    // phase 10.20: base radar, widened by up_scanner (L3)
		CargoBay:       float64(cls.CargoBay), // phase 10.3.17: hold capacity from class, widened by up_cargobay (10.3.16)
	}
}

// spawnStarter persists a starter ship (full class stats + missile cargo) at
// the given location and mirrors it into the sector's worker. When the M5
// catalog entry is available for the race, it supplies the combat stats
// (speed, hull, shield, laser); otherwise falls back to cfg values.
func (s *shipSpawner) spawnStarter(ctx context.Context, playerID domain.PlayerID, race domain.RaceID, name string, sectorID domain.SectorID, pos domain.Vec2) error {
	maxSpeed := s.cfg.StartMaxSpeed
	accel := s.cfg.StartAccel
	hp := s.cfg.StartHP
	shield := s.cfg.StartShld
	shieldCharge := s.cfg.StartShldCharge
	laserDmg := s.cfg.StartLaserDamage
	var classID domain.ShipClassID
	var radarRange float64 // 0 → subscription falls back to cfg.AOIRadius
	cargoBay := 100.0      // phase 10.3.17: class overrides below; 100 keeps the legacy hold for classless ships

	if cls, ok := s.starterClass(race); ok && cls.Hull > 0 {
		maxSpeed = cls.Speed
		accel = cls.Acceleration
		hp = cls.Hull
		shield = cls.Shield
		shieldCharge = cls.ShieldCharge
		laserDmg = cls.Laser / warshipLaserDivisor
		if laserDmg < s.cfg.StartLaserDamage {
			laserDmg = s.cfg.StartLaserDamage
		}
		classID = cls.ID
		radarRange = float64(cls.Radar)  // phase 10.20 L1
		cargoBay = float64(cls.CargoBay) // phase 10.3.17: hold capacity from class
	}

	ship := domain.Ship{
		PlayerID:        playerID,
		Race:            race,
		Name:            name,
		ShipClassID:     classID,
		SectorID:        sectorID,
		Pos:             pos,
		MaxSpeed:        maxSpeed,
		Acceleration:    accel,
		TurnRate:        s.cfg.StartTurnRate,
		Direction:       domain.Vec2{X: 1, Y: 0},
		HP:              hp,
		MaxHP:           hp,
		Shield:          shield,
		MaxShield:       shield,
		ShieldRecharge:  shieldCharge,
		Energy:          s.cfg.StartEnergy,
		MaxEnergy:       s.cfg.StartEnergy,
		EnergyRecharge:  s.cfg.StartEnergyChrg,
		LaserDamage:     laserDmg,
		LaserRange:      s.cfg.StartLaserRange,
		LaserEnergyCost: s.cfg.StartLaserECost,
		RadarRange:      radarRange,
		CargoBay:        cargoBay,
	}
	id, err := s.repo.Create(ctx, ship)
	if err != nil {
		return fmt.Errorf("ship insert: %w", err)
	}
	ship.ID = id

	// Phase 4.3: seed starter missile cargo. cargo is optional — a nil
	// cargo repo (unit tests, minimal deployments) silently skips the
	// step so legacy bring-up stays unaffected.
	if s.cargo != nil && s.cfg.StartMissiles > 0 {
		owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(id)}
		if err := s.cargo.Add(ctx, owner, MissileGoodsType, s.cfg.StartMissiles, 0); err != nil {
			return fmt.Errorf("seed missile cargo: %w", err)
		}
	}

	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sectorID, sector.AddShipCommand{Ship: ship, Reply: reply}); err != nil {
		return fmt.Errorf("worker send add-ship: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.AckTimeout)
	defer cancel()
	select {
	case res := <-reply:
		if res.Err != nil && !errors.Is(res.Err, sector.ErrShipExists) {
			return fmt.Errorf("worker add-ship: %w", res.Err)
		}
		return nil
	case <-waitCtx.Done():
		return errors.New("worker add-ship: ack timeout")
	}
}

// SpawnSpacesuit creates a weak spacesuit ship for the player at the given
// sector/position and mirrors it into that sector's worker. No starter cargo.
// Used both for death (phase 10.1, docked=nil) and voluntary exit/disembark
// (phase 10.23: docked carries the station the player was parked at so the suit
// stays in the hangar). Returns the new ship id. The player keeps flying in the
// same sector their WS is already on, so the caller only re-points active_ship_id
// — no handoff is needed.
func (s *shipSpawner) SpawnSpacesuit(ctx context.Context, playerID domain.PlayerID, sectorID domain.SectorID, pos domain.Vec2, docked *domain.EntityRef) (domain.ShipID, error) {
	ship := domain.Ship{
		PlayerID:        playerID,
		SectorID:        sectorID,
		Pos:             pos,
		Direction:       domain.Vec2{X: 1, Y: 0},
		MaxSpeed:        s.cfg.SpacesuitMaxSpeed,
		Acceleration:    s.cfg.StartAccel,
		TurnRate:        s.cfg.StartTurnRate,
		HP:              s.cfg.SpacesuitHP,
		MaxHP:           s.cfg.SpacesuitHP,
		Energy:          s.cfg.StartEnergy,
		MaxEnergy:       s.cfg.StartEnergy,
		EnergyRecharge:  s.cfg.StartEnergyChrg,
		LaserDamage:     s.cfg.SpacesuitLaserDamage,
		LaserRange:      s.cfg.StartLaserRange,
		LaserEnergyCost: s.cfg.StartLaserECost,
		IsSpacesuit:     true,
		Docked:          docked,
	}
	id, err := s.repo.Create(ctx, ship)
	if err != nil {
		return 0, fmt.Errorf("spacesuit insert: %w", err)
	}
	ship.ID = id

	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sectorID, sector.AddShipCommand{Ship: ship, Reply: reply}); err != nil {
		return 0, fmt.Errorf("worker send spacesuit: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.AckTimeout)
	defer cancel()
	select {
	case res := <-reply:
		if res.Err != nil && !errors.Is(res.Err, sector.ErrShipExists) {
			return 0, fmt.Errorf("worker spacesuit: %w", res.Err)
		}
		return id, nil
	case <-waitCtx.Done():
		return 0, errors.New("worker spacesuit: ack timeout")
	}
}

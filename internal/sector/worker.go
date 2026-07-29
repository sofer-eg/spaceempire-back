package sector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/world"
)

var ErrInboxFull = errors.New("sector: inbox full")

// ShipRepo is the minimal persistence surface a worker needs. The real
// implementation lives in internal/persistence/ships; tests pass mocks.
// Passing a nil ShipRepo disables persistence (handy for pure unit tests).
type ShipRepo interface {
	Save(ctx context.Context, ship domain.Ship) error
	// SaveEquipment persists a ship's Equipment list, the stat columns it folds
	// into (max_shield/shield_recharge/…) and the shield_generator_destroyed
	// marker (TASK-100.3.9.1). Save/BatchUpdate write only dynamic fields, so a
	// combat knockoff must go through this path or it is lost at cold-start.
	SaveEquipment(ctx context.Context, ship domain.Ship) error
	BatchUpdate(ctx context.Context, ships []domain.Ship) error
	Delete(ctx context.Context, id domain.ShipID) error
}

// DroneRepo is the persistence surface for combat drones (phase 4.4).
// The real implementation lives in internal/persistence/drones. Wired in
// via WithDrones; nil disables drone persistence — drones still fly but
// their updates/deaths are not written (used by unit tests). Launch INSERTs do
// NOT go through here: they run through Ordnance, which creates the rows in the
// same transaction as the ammunition debit (TASK-147).
type DroneRepo interface {
	BatchUpdate(ctx context.Context, ds []domain.Drone) error
	Delete(ctx context.Context, id domain.DroneID) error
}

// TorpedoRepo is the persistence surface for homing torpedoes (TASK-100.3.5).
// The real implementation lives in internal/persistence/torpedos. Wired via
// WithTorpedos; nil disables persistence (used by unit tests). Mirrors
// DroneRepo, including that launch INSERTs go through Ordnance instead
// (TASK-147).
type TorpedoRepo interface {
	BatchUpdate(ctx context.Context, ts []domain.Torpedo) error
	Delete(ctx context.Context, id domain.TorpedoID) error
}

// RNG is the randomness source the kill handler needs for the SP cargo
// drop rolls (see kill_object.md §3). Aliased to combat.RNG so a single
// source threads through. *math/rand.Rand satisfies it.
type RNG = combat.RNG

// TowerRepo persists laser-tower destruction (phase 8.5): when a tower is
// killed it is deleted so cold-start does not reload it. Nil disables it
// (RAM-only kill, restored on restart). Wired via WithTowerPersistence.
type TowerRepo interface {
	Delete(ctx context.Context, id domain.LaserTowerID) error
}

// SatelliteRepo persists the destruction of player-deployed navigation
// satellites (phase 10.15): Delete on death so a restart does not resurrect a
// killed satellite. Nil makes a kill RAM-only — handy for pure unit tests. Wired
// via WithSatellites. Installs do NOT go through here: they run through
// StaticInstaller, which creates the row in the same transaction as the goods
// debit (TASK-144).
type SatelliteRepo interface {
	Delete(ctx context.Context, id domain.SatelliteID) error
}

// JammerRepo persists the destruction of player-deployed hyper-interference
// generators (TASK-131): Delete on death so a restart does not resurrect a
// killed generator. Nil makes a kill RAM-only — handy for pure unit tests. Wired
// via WithJammers. Installs go through StaticInstaller, not here (TASK-144).
type JammerRepo interface {
	Delete(ctx context.Context, id domain.JammerID) error
}

// ContainerRepo is the persistence surface for loot containers (phase
// 4.6). The real implementation lives in internal/persistence/containers.
// Wired via WithContainers; nil disables persistence — a dead ship is
// still swept from RAM but nothing is written and no container drops.
type ContainerRepo interface {
	// ShipCargo lists the victim's cargo so the kill handler can plan the
	// drop before the row is deleted.
	ShipCargo(ctx context.Context, ship domain.ShipID) ([]domain.CargoItem, error)
	// RecordKill atomically deletes the victim ship + its leftover cargo
	// and creates one container (with its cargo) per drop, returning the
	// created containers.
	RecordKill(ctx context.Context, victim domain.ShipID, sectorID domain.SectorID, drops []domain.ContainerDrop) ([]domain.Container, error)
	// SpawnContainer creates one container (with its cargo) in space without
	// a kill — used by player mining (phase 10.3.6) to drop drilled ore.
	SpawnContainer(ctx context.Context, sectorID domain.SectorID, drop domain.ContainerDrop) (domain.Container, error)
	// Pickup moves a container's cargo into the ship (capacity-checked,
	// all-or-nothing) and deletes the container.
	Pickup(ctx context.Context, container domain.ContainerID, ship domain.ShipID) error
	// Delete removes an expired container and its cargo (TTL sweep).
	Delete(ctx context.Context, id domain.ContainerID) error
}

// AsteroidRepo is the persistence surface for minable asteroids (phase 5.4).
// The real implementation lives in internal/persistence/asteroids. Wired via
// WithAsteroids; nil disables persistence — asteroids still mine down in RAM
// but their mass is not restored after a restart (used by unit tests).
type AsteroidRepo interface {
	BatchUpdate(ctx context.Context, as []domain.Asteroid) error
	Delete(ctx context.Context, id domain.AsteroidID) error
}

// MinerLogistics executes an NPC miner's ai.Mine deposit: it adds qty units
// of ore (the drilled goods) into the miner ship's hold in a single
// transaction, capacity-checked. Wired via WithMinerLogistics; nil makes the
// ore-deposit half of Mine a no-op (the asteroid still mines down). The real
// implementation lives in app/ (over cargo.Service), keeping the sector
// package free of cargo dependencies. Phase 5.4.
type MinerLogistics interface {
	AddOre(ctx context.Context, ship domain.EntityRef, ore domain.GoodsTypeID, qty int64) error
}

// StaticInstaller deploys a player-owned static object and charges its goods in
// ONE transaction (TASK-144): the cargo debit and the object INSERT commit
// together, so a lost ack can never yield a free jammer/satellite and a failed
// insert can never eat the goods. owner is the installing ship's cargo hold and
// gtype the goods id the command carries (the sector package stays free of the
// goods catalog). Wired via WithStaticInstaller.
//
// This is the ONLY install path. A worker without one refuses install commands
// with ErrInstallerUnavailable rather than creating the object for free: the
// installer is what makes the player pay, so losing it in a refactor must break
// loudly instead of handing out ≈1.13M cr generators.
//
// The real implementation lives in app/ over cargo + the object repositories,
// keeping the sector package free of cargo dependencies (mirrors
// MinerLogistics).
type StaticInstaller interface {
	InstallJammer(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, j domain.Jammer) (domain.JammerID, error)
	InstallSatellite(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, s domain.Satellite) (domain.SatelliteID, error)
}

// Ordnance charges a launch's ammunition and creates its projectile rows in ONE
// transaction (TASK-147), the same discipline StaticInstaller brought to installs:
// the cargo debit and the INSERTs commit together, so a lost ack can never yield
// a free missile/torpedo/drone and a failed insert can never eat the ammunition.
// owner is the launching ship's cargo hold and gtype the goods id the command
// carries (the sector package stays free of the goods catalog). Wired via
// WithOrdnance.
//
// SpendMissile has no object to create — a missile is RAM-only, reconstructable
// state — so its "transaction" is the debit alone. LaunchDrones takes the whole
// prepared salvo and is all-or-nothing: it returns one id per drone, in order, or
// an error and nothing charged. That is what makes a partial spawn impossible.
//
// RecallDrones runs the same transaction backwards (TASK-152): it deletes the
// drone rows and credits the units in one commit, so a lost ack cannot delete a
// player's drones and pay nothing back. It returns how many units were credited
// — one per row it actually deleted, which can be fewer than len(ids) when a row
// is already gone (see recallDrones).
//
// This is the ONLY launch path, and the only recall path. A worker without one
// refuses every launch with ErrOrdnanceUnavailable rather than firing for free,
// and every recall rather than deleting drones uncredited: the ordnance is what
// makes the player pay and what pays the player back, so losing it in a refactor
// must break loudly.
//
// The real implementation lives in app/ over cargo + the projectile repositories,
// keeping the sector package free of cargo dependencies (mirrors StaticInstaller).
type Ordnance interface {
	SpendMissile(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID) error
	LaunchTorpedo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, t domain.Torpedo) (domain.TorpedoID, error)
	LaunchDrones(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, ds []domain.Drone) ([]domain.DroneID, error)
	RecallDrones(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, ids []domain.DroneID) (int, error)
}

// Relations is the worker's hostility oracle (phase 6.2a): ship-vs-ship
// standing for laser friendly-fire gating and drone auto-acquire. Injected
// via WithRelations; nil keeps the pre-6.2a behaviour (lasers fire at any
// set target, drones never auto-acquire) so pure unit tests need no wiring.
// Satisfied by *relations.Service.
type Relations interface {
	IsHostile(a, b domain.EntityRef) bool
	Get(a, b domain.EntityRef) domain.Relation
}

// HangerStats resolves a ship class's hangar capacity and footprint (phase
// 10.3.24): the worker needs it to gate ship-to-ship docking (whether a host
// ship can carry the docking ship in its hangar). Keyed by ship.ShipClassID —
// the data lives in balance.ShipClass and is immutable per class, so no per-row
// denormalization is needed. Injected via WithHangerStats; nil disables
// ship-to-ship docking (every ship resolves to the zero Hanger → no hangar).
// Satisfied by an app-side adapter over *balance.ShipClasses.
type HangerStats interface {
	HangerOf(classID domain.ShipClassID) domain.Hanger
}

// AIStateRepo is the persistence surface for NPC AI controller state
// (phase 5.1). The real implementation lives in internal/persistence/
// aistate. Wired via WithAI together with the registry and the cold-start
// AIState set; nil disables AI-state persistence — controllers still tick,
// but their phase is not saved across restarts (used by pure unit tests).
type AIStateRepo interface {
	BatchUpsert(ctx context.Context, states []domain.AIState) error
}

// envelope is the inbox queue item: a command plus the sector it targets.
type envelope struct {
	sectorID domain.SectorID
	cmd      Command
}

// ProductionTicker advances per-sector station production every tick. It
// is wired in via WithProduction; nil disables the production step. The
// real implementation lives in internal/economy/production.
type ProductionTicker interface {
	Tick(ctx context.Context, stations []domain.Station, now time.Time) (int, error)
}

// TraderLogistics executes an NPC trader's ai.Transfer: it hauls up to
// maxUnits of gtype from one cargo owner to another (station↔ship) in a
// single transaction, moving min(available-at-from, maxUnits, room-in-to).
// Wired via WithTraderLogistics; nil makes Transfer a no-op. The real
// implementation lives in app/ (over cargo.Service + balance), so the
// sector package stays free of cargo/balance dependencies. Phase 5.3.
type TraderLogistics interface {
	Haul(ctx context.Context, from, to domain.EntityRef, gtype domain.GoodsTypeID, maxUnits int64) error
}

// Worker owns the live state of one or more sectors in RAM. It runs a single
// tick goroutine that processes commands from inbox, advances each sector
// every TickInterval, broadcasts patches to subscribers, and periodically
// flushes dirty ships to the repo. The one-writer-per-sector invariant: only
// the tick goroutine mutates sectorState; everything else interacts via the
// inbox or the atomic Snapshot pointer.
type Worker struct {
	idx    int
	cfg    Config
	clock  clock.Clock
	repo   ShipRepo
	logger *slog.Logger

	// droneRepo persists combat drones. Nil disables drone persistence.
	// Wired in via WithDrones together with initialDrones.
	droneRepo DroneRepo

	// torpedoRepo persists homing torpedoes (TASK-100.3.5). Nil disables
	// torpedo persistence. Wired in via WithTorpedos together with
	// initialTorpedos.
	torpedoRepo TorpedoRepo

	// containerRepo persists loot containers (phase 4.6). Nil disables
	// persistence (ships still die, but no container drops). Wired via
	// WithContainers together with initialContainers.
	containerRepo ContainerRepo

	// towerRepo persists laser-tower destruction (phase 8.5). Nil disables it
	// (a killed tower is restored on restart). Wired via WithTowerPersistence.
	towerRepo TowerRepo

	// satelliteRepo persists navigation-satellite destruction (phase 10.15).
	// Nil makes a kill RAM-only. Wired via WithSatellites.
	satelliteRepo SatelliteRepo

	// jammerRepo persists hyper-interference-generator destruction (TASK-131).
	// Nil makes a kill RAM-only. Wired via WithJammers.
	jammerRepo JammerRepo

	// staticInstaller charges the goods and creates the object in one
	// transaction — the only install path (TASK-144). Nil makes install
	// commands fail with ErrInstallerUnavailable. Wired via WithStaticInstaller.
	staticInstaller StaticInstaller

	// ordnance charges a launch's ammunition and creates its projectile rows in
	// one transaction — the only launch path (TASK-147). Nil makes launch-missile
	// / launch-torpedo / launch-drone fail with ErrOrdnanceUnavailable. Wired via
	// WithOrdnance.
	ordnance Ordnance

	// dbBudget is the DB time the commands that write synchronously may still
	// spend in the current inbox drain. Config.RepoTimeout bounds ONE such call;
	// without a per-drain budget a full inbox of them against a hung Postgres
	// would park the Run goroutine — and with it every sector this worker owns —
	// with no tick in between, which any player can trigger since an install or a
	// launch with an empty hold now reaches the worker. Reset at the start of
	// every drain; charged by every DB call the worker makes (TASK-148 routed all
	// of them through dbCall, where TASK-144/147/152 charged only the install,
	// launch and recall paths), so a pickup, a hack or a dock now bounds a drain
	// exactly like an install does. It gates whether the drain continues, never
	// the per-command deadline: clamping that would fail a legal command with a
	// spurious DeadlineExceeded. ONE drain is therefore bounded by
	// ~2 × RepoTimeout.
	//
	// What this does NOT bound is the total: Run resets the budget on every
	// wake-up (applyAndDrain), and the overflow is still sitting in the inbox, so
	// the queue is worked through at ~RepoTimeout per command either way. The
	// degradation window stays InboxCapacity × RepoTimeout (256 × 2 s ≈ 8.5 min)
	// and a command queued behind it still waits that long for its ack. What the
	// budget buys is that Run returns to its select between those payments, so the
	// sectors keep ticking at a reduced rate (measured ~40-80% of nominal, against
	// a single tick with the budget disabled) instead of the goroutine being parked
	// outright. Bounding the total — moving these writes off the tick goroutine
	// altogether — is still open; TASK-148 bounded every individual call instead.
	dbBudget time.Duration

	// dbSince measures what one synchronous DB call really cost, and is the only
	// thing that charges dbBudget (see dbCallCost/spendDBBudget). Nil means the
	// wall clock, which is what production runs on; tests set it through
	// WithDBDurationSource so the budget arithmetic does not depend on how long a
	// fake call happens to take under scheduler load (TASK-154).
	dbSince func(started time.Time) time.Duration

	// asteroidRepo persists minable asteroids (phase 5.4). Nil disables
	// persistence (asteroids still mine down in RAM). Wired via
	// WithAsteroids together with initialAsteroids.
	asteroidRepo AsteroidRepo

	// rng feeds the kill handler's cargo drop rolls. Defaults to a clock-
	// seeded math/rand source in NewWorker; tests inject a deterministic
	// one via WithRNG.
	rng RNG

	// hostile decides whether a ship is a valid laser-tower target.
	// Defaults to combat.NoHostility (phase 4.5 stub: nobody is hostile
	// until relations 6.2). Override in tests via WithHostility; app wires a
	// relations-backed predicate in 6.2a.
	hostile combat.HostilePredicate

	// raceHostile decides whether a race-owned (owner==nil) laser tower of
	// the given race fires at a ship (phase 8.3). Nil leaves race-owned towers
	// passive (pre-8.3 behaviour). app wires a predicate where the hostile
	// races (pirate/xenon/kha'ak) fire at real-player ships.
	raceHostile func(race int, ship *domain.Ship) bool

	// relations is the ship-vs-ship hostility oracle (6.2a) used by laser
	// friendly-fire gating and drone auto-acquire. Nil disables both.
	relations Relations

	// hangers resolves a ship class's hangar capacity/footprint for
	// ship-to-ship docking (phase 10.3.24). Nil disables ship-to-ship docking
	// (the capacity check treats every ship as having no hangar). Wired via
	// WithHangerStats.
	hangers HangerStats

	// police runs the per-tick contraband scan (phase 9.4); policeRaces is the
	// set of races whose navy acts as police; policeCfg tunes range/cooldown.
	// Nil police or empty policeRaces disables the step. Wired via WithPolice.
	police      PoliceScanner
	policeRaces map[domain.RaceID]bool
	policeCfg   PoliceConfig

	// reputation grants war_rate to a kill's attributed player (phase 10.3.13).
	// Nil disables the accrual. Wired via WithReputation. The app implementation
	// applies the delta via players.AddReputation and skips NPC/zero killers,
	// mirroring the PoliceScanner split.
	reputation ReputationAwarder

	// refit recomputes a ship's folded stat fields from its Equipment after a
	// module is knocked off in combat (TASK-100.3.9.1). Nil leaves the knocked
	// ship's stats untouched (the shield-generator collapse is handled inline,
	// independent of this). Wired via WithRefit over the balance catalog.
	refit Refitter

	// robber runs the DB side of a station hack (TASK-100.3.9.3, SP UseHack):
	// deduct the station's stock, deposit loot into the hold, drop the hacker's
	// race standing. Nil disables HackStationCommand's effect (pure unit tests).
	// Wired via WithStationRobber over trade.Service + racestanding.
	robber StationRobber

	// Handoff dependencies — both nil disables JumpCommand handling and
	// intake subscriptions. Wired in via WithHandoff option.
	topology *world.Topology
	bus      bus.Bus

	// Routing dependency — when set, the autopilot resolves FinalTarget
	// into Target every tick and the tick loop fires auto-jumps through
	// gates. Nil disables the player autopilot; ships only honour Target
	// set by MoveCommand or JumpCommand.
	router PathRouter

	// production runs the per-sector factory cycle. Nil disables the step.
	production ProductionTicker

	// traderLogistics executes NPC traders' ai.Transfer (cargo haul) in
	// applyAIAction. Nil makes Transfer a no-op. Wired via
	// WithTraderLogistics (phase 5.3).
	traderLogistics TraderLogistics

	// minerLogistics deposits drilled ore into a miner's hold for ai.Mine.
	// Nil makes the ore-deposit half of Mine a no-op. Wired via
	// WithMinerLogistics (phase 5.4).
	minerLogistics MinerLogistics

	// initialStatics is the per-sector static-object set supplied via
	// WithStatics. NewWorker consumes it once into sectorState and clears
	// the reference — it is never read after construction.
	initialStatics map[domain.SectorID]domain.SectorStatics

	// initialDrones is the per-sector cold-start drone set supplied via
	// WithDrones. NewWorker consumes it once into sectorState and clears
	// the reference.
	initialDrones map[domain.SectorID][]domain.Drone

	// initialTorpedos is the per-sector cold-start torpedo set supplied via
	// WithTorpedos. NewWorker consumes it once into sectorState and clears
	// the reference, so live torpedoes survive a restart (ЧТЗ NFR-002).
	initialTorpedos map[domain.SectorID][]domain.Torpedo

	// initialContainers is the per-sector cold-start loot-container set
	// supplied via WithContainers. NewWorker consumes it once into
	// sectorState and clears the reference.
	initialContainers map[domain.SectorID][]domain.Container

	// initialAsteroids is the per-sector cold-start asteroid set supplied
	// via WithAsteroids. NewWorker consumes it once into sectorState and
	// clears the reference.
	initialAsteroids map[domain.SectorID][]domain.Asteroid

	// AI runtime dependencies (phase 5.1). aiRegistry rebuilds controllers
	// from persisted ai_state at cold-start; aiStateRepo snapshots their
	// state; initialAIState is the per-sector cold-start AIState set. All
	// nil disables AI — buildControllers still runs (leaving an empty
	// controller map) so the tick step is a cheap no-op. Wired via WithAI.
	aiRegistry     *ai.Registry
	aiStateRepo    AIStateRepo
	initialAIState map[domain.SectorID][]domain.AIState

	// metrics receives per-tick telemetry (phase 7.1). Defaults to
	// noopMetrics in NewWorker; app wires the Prometheus-backed sink via
	// WithMetrics.
	metrics MetricsSink

	inbox    chan envelope
	sectors  map[domain.SectorID]*sectorState
	subIDSeq uint64

	overruns      atomic.Uint64
	intakeSubOnce sync.Once
}

// Option configures optional Worker dependencies.
type Option func(*Worker)

// WithHandoff enables sector handoff: the worker will validate JumpCommand
// against the topology and subscribe to its owned sectors' intake topics
// on the bus once Run starts. Passing nil topology or bus is treated as
// "handoff disabled".
func WithHandoff(topo *world.Topology, b bus.Bus) Option {
	return func(w *Worker) {
		w.topology = topo
		w.bus = b
	}
}

// WithRouter enables the player autopilot. The worker will resolve each
// ship's FinalTarget into a per-tick waypoint and auto-jump through gates
// along the shortest route returned by the router. Without WithHandoff, an
// auto-jump cannot complete (no bus), so this option only has an effect
// when WithHandoff is also set.
func WithRouter(r PathRouter) Option {
	return func(w *Worker) {
		w.router = r
	}
}

// WithStatics supplies the cold-start static objects (stations, shipyards,
// trade stations, pirbases) per sector. Missing keys default to an empty
// SectorStatics. The map is consumed during NewWorker only.
func WithStatics(statics map[domain.SectorID]domain.SectorStatics) Option {
	return func(w *Worker) {
		w.initialStatics = statics
	}
}

// WithProduction enables the per-tick station production cycle.
func WithProduction(p ProductionTicker) Option {
	return func(w *Worker) {
		w.production = p
	}
}

// WithDrones enables persistent combat drones: the worker writes launch
// INSERTs / death DELETEs immediately and the periodic snapshot batch.
// initial is the per-sector cold-start drone set (LoadAll'd by the
// caller); missing keys start empty. Passing a nil repo with a non-nil
// initial still seeds the live set but never persists changes.
func WithDrones(repo DroneRepo, initial map[domain.SectorID][]domain.Drone) Option {
	return func(w *Worker) {
		w.droneRepo = repo
		w.initialDrones = initial
	}
}

// WithTorpedos enables persistent homing torpedoes: the worker writes launch
// INSERTs / death DELETEs immediately and the periodic snapshot batch. initial
// is the per-sector cold-start torpedo set (LoadAll'd by the caller); missing
// keys start empty. Passing a nil repo with a non-nil initial still seeds the
// live set but never persists changes. Mirrors WithDrones.
func WithTorpedos(repo TorpedoRepo, initial map[domain.SectorID][]domain.Torpedo) Option {
	return func(w *Worker) {
		w.torpedoRepo = repo
		w.initialTorpedos = initial
	}
}

// WithContainers enables loot containers: the worker drops a dead ship's
// cargo into containers (immediate, transactional writes), serves pickup
// commands, and sweeps containers past their TTL. initial is the
// per-sector cold-start container set (LoadAll'd by the caller); missing
// keys start empty. A nil repo disables persistence entirely.
func WithContainers(repo ContainerRepo, initial map[domain.SectorID][]domain.Container) Option {
	return func(w *Worker) {
		w.containerRepo = repo
		w.initialContainers = initial
	}
}

// WithTowerPersistence enables persisting laser-tower destruction (phase 8.5):
// a killed tower's row is deleted so a restart does not resurrect it. Without
// it, tower kills are RAM-only.
func WithTowerPersistence(repo TowerRepo) Option {
	return func(w *Worker) {
		w.towerRepo = repo
	}
}

// WithSatellites persists the destruction of player-deployed navigation
// satellites (phase 10.15): killStatic deletes the row so a restart does not
// resurrect it. A nil repo keeps the kill RAM-only, used by pure unit tests.
// Installs are handled by WithStaticInstaller, not here.
func WithSatellites(repo SatelliteRepo) Option {
	return func(w *Worker) {
		w.satelliteRepo = repo
	}
}

// WithJammers persists the destruction of player-deployed hyper-interference
// generators (TASK-131): killStatic deletes the row so a restart does not
// resurrect it. A nil repo keeps the kill RAM-only, used by pure unit tests.
// Installs are handled by WithStaticInstaller, not here.
func WithJammers(repo JammerRepo) Option {
	return func(w *Worker) {
		w.jammerRepo = repo
	}
}

// WithStaticInstaller injects the transactional installer the install-jammer /
// install-satellite commands use to charge the goods and create the object
// together (TASK-144). It is the only install path: without it those commands
// fail with ErrInstallerUnavailable.
func WithStaticInstaller(i StaticInstaller) Option {
	return func(w *Worker) {
		w.staticInstaller = i
	}
}

// WithOrdnance injects the transactional ammunition charger the launch-missile /
// launch-torpedo / launch-drone commands use to debit the hold and create the
// projectile rows together (TASK-147). It is the only launch path: without it
// those commands fail with ErrOrdnanceUnavailable.
func WithOrdnance(o Ordnance) Option {
	return func(w *Worker) {
		w.ordnance = o
	}
}

// WithAI enables the NPC AI runtime: controllers are rebuilt from the
// cold-start AIState set via the registry, ticked every sector tick, and
// snapshotted to the repo on the SnapshotInterval cadence (and on graceful
// shutdown). initial is the per-sector AIState set (LoadAll'd by the
// caller); missing keys start with no controllers. Passing a nil registry
// leaves every controller unbuilt; a nil repo disables persistence but
// still ticks whatever controllers were built.
func WithAI(registry *ai.Registry, repo AIStateRepo, initial map[domain.SectorID][]domain.AIState) Option {
	return func(w *Worker) {
		w.aiRegistry = registry
		w.aiStateRepo = repo
		w.initialAIState = initial
	}
}

// WithTraderLogistics injects the cargo-haul executor NPC traders use for
// ai.Transfer (phase 5.3). Nil leaves Transfer a no-op (unit tests without
// a DB). The implementation lives in app/ over cargo.Service + balance.
func WithTraderLogistics(l TraderLogistics) Option {
	return func(w *Worker) {
		w.traderLogistics = l
	}
}

// WithAsteroids enables minable asteroids: the worker mines them down on
// ai.Mine (immediate Delete when depleted, periodic BatchUpdate of mass, and
// a shutdown flush). initial is the per-sector cold-start asteroid set
// (LoadAll'd by the caller); missing keys start empty. A nil repo disables
// persistence (asteroids still mine down in RAM). Phase 5.4.
func WithAsteroids(repo AsteroidRepo, initial map[domain.SectorID][]domain.Asteroid) Option {
	return func(w *Worker) {
		w.asteroidRepo = repo
		w.initialAsteroids = initial
	}
}

// WithMinerLogistics injects the ore-deposit executor NPC miners use for
// ai.Mine (phase 5.4). Nil leaves the ore-deposit half of Mine a no-op (unit
// tests without a DB). The implementation lives in app/ over cargo.Service.
func WithMinerLogistics(l MinerLogistics) Option {
	return func(w *Worker) {
		w.minerLogistics = l
	}
}

// WithRelations injects the ship-vs-ship hostility oracle (phase 6.2a) for
// laser friendly-fire gating and drone auto-acquire. Nil keeps the pre-6.2a
// behaviour.
func WithRelations(r Relations) Option {
	return func(w *Worker) {
		w.relations = r
	}
}

// WithHangerStats injects the ship-class hangar resolver used to gate
// ship-to-ship docking (phase 10.3.24). Nil disables ship-to-ship docking.
func WithHangerStats(h HangerStats) Option {
	return func(w *Worker) {
		w.hangers = h
	}
}

// WithRNG overrides the kill handler's randomness source. Production
// leaves it unset (NewWorker seeds a math/rand source from the clock);
// tests inject a deterministic RNG to pin the cargo drop rolls.
func WithRNG(rng RNG) Option {
	return func(w *Worker) {
		w.rng = rng
	}
}

// WithHostility overrides the laser-tower hostility predicate. Production
// leaves it unset (defaulting to combat.NoHostility) until relations (6.2)
// land; tests inject an owner-based predicate to exercise tower fire.
func WithHostility(p combat.HostilePredicate) Option {
	return func(w *Worker) {
		w.hostile = p
	}
}

// WithRaceHostility wires the predicate that activates race-owned (owner==nil)
// laser towers (phase 8.3): given the tower's race and a ship, report whether
// the tower fires. Without it, race-owned towers stay passive.
func WithRaceHostility(p func(race int, ship *domain.Ship) bool) Option {
	return func(w *Worker) {
		w.raceHostile = p
	}
}

// NewWorker builds an in-memory worker over the given initial ship sets. The
// initial map's keys define which sectors this worker owns; pass an empty
// slice for sectors that start empty. repo and logger are optional — pass
// nil for either to opt out (e.g. in pure unit tests).
func NewWorker(
	idx int,
	cfg Config,
	clk clock.Clock,
	repo ShipRepo,
	logger *slog.Logger,
	initial map[domain.SectorID][]domain.Ship,
	opts ...Option,
) *Worker {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	w := &Worker{
		idx:     idx,
		cfg:     cfg,
		clock:   clk,
		repo:    repo,
		logger:  logger.With("component", "sector.worker", "worker", idx),
		inbox:   make(chan envelope, cfg.InboxCapacity),
		sectors: make(map[domain.SectorID]*sectorState, len(initial)),
		metrics: noopMetrics{},
	}
	for _, opt := range opts {
		opt(w)
	}
	if w.hostile == nil {
		w.hostile = combat.NoHostility
	}

	now := clk.Now()
	if w.rng == nil {
		// Loot RNG, not security-sensitive: a clock-seeded math/rand source
		// is enough for cargo drop rolls.
		w.rng = rand.New(rand.NewSource(now.UnixNano())) //nolint:gosec // loot RNG, not security-sensitive
	}
	for id, ships := range initial {
		w.sectors[id] = newSectorState(id, ships, w.initialDrones[id], w.initialTorpedos[id], w.initialContainers[id], w.initialAsteroids[id], w.initialStatics[id], now)
	}
	w.initialStatics = nil
	w.initialDrones = nil
	w.initialTorpedos = nil
	w.initialContainers = nil
	w.initialAsteroids = nil

	// Hydrate AI controllers for every owned sector (always — buildControllers
	// initializes an empty map when no AI is wired, keeping tickAI a no-op).
	for id, s := range w.sectors {
		w.buildControllers(s, w.initialAIState[id])
	}
	w.initialAIState = nil

	return w
}

// Sectors returns the SectorIDs owned by this worker. Returned slice order
// is undefined.
func (w *Worker) Sectors() []domain.SectorID {
	out := make([]domain.SectorID, 0, len(w.sectors))
	for id := range w.sectors {
		out = append(out, id)
	}
	return out
}

// Send queues a command for the given sector. Returns ErrSectorNotFound if
// this worker does not own the sector, or ErrInboxFull when the inbox cannot
// accept the message.
func (w *Worker) Send(sectorID domain.SectorID, cmd Command) error {
	if _, ok := w.sectors[sectorID]; !ok {
		return ErrSectorNotFound
	}
	select {
	case w.inbox <- envelope{sectorID: sectorID, cmd: cmd}:
		return nil
	default:
		return ErrInboxFull
	}
}

// Snapshot returns the most recent published snapshot for the sector. The
// returned value is a deep copy — callers may mutate it freely. A sector the
// worker does not own returns a zero Snapshot.
func (w *Worker) Snapshot(sectorID domain.SectorID) Snapshot {
	s, ok := w.sectors[sectorID]
	if !ok {
		return Snapshot{}
	}
	return *s.snap.Load()
}

func (w *Worker) Run(ctx context.Context) error {
	w.EnsureSubscriptions(ctx)

	ticker := w.clock.NewTicker(w.cfg.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.flushAll()
			return nil
		case <-ticker.C():
			w.Tick(ctx)
		case env := <-w.inbox:
			// Apply queued commands as they arrive instead of letting them sit
			// until the next Tick drains them — otherwise a command can wait up
			// to a full TickInterval (3s) for its ack, which the spawner only
			// allows AckTimeout (1s) for, producing a spurious "add-ship ack
			// timeout" on player registration. Mutation runs on this same Run
			// goroutine (the one-writer-per-sector invariant holds); the change
			// is published on the next tick's snapshot, exactly as a
			// start-of-tick drain would have.
			w.applyAndDrain(env)
		}
	}
}

// saveShip persists one ship through the Save path under the in-tick DB
// contract (see dbCall). Returns nil when persistence is disabled (w.repo ==
// nil, pure unit tests) so callers need no separate guard. Takes the ship by
// value: handoff saves a relocated copy and the capture saves a prospective
// one, neither of which is the live RAM ship yet.
//
// The parent is always context.Background(), not the tick's context, and that is
// not an oversight: every caller (immediateSave, the docking/access saves, the
// handoff, the ownership transfer) is a command-driven write with no context of
// its own, and the two tick-path callers reach it through immediateSave from the
// AI action handler. A shutdown therefore does not cut a ship save short — it
// waits out at most one RepoTimeout, which is the right trade for the write that
// carries ownership and docking state.
func (w *Worker) saveShip(ship domain.Ship) error {
	if w.repo == nil {
		return nil
	}
	return w.dbCall(context.Background(), func(ctx context.Context) error {
		return w.repo.Save(ctx, ship)
	})
}

// immediateSave wraps the optional repo.Save with the same logging
// convention used by flushAll: a save failure is logged but never
// propagated to the caller — at worst the next periodic BatchUpdate
// (or the shutdown flush) carries the change. Used for command-driven
// immediate writes (AttackCommand, CeaseFireCommand) where the player
// has already moved on to the next click by the time we return.
//
// "At worst the next BatchUpdate" is true only for the fields BatchUpdate
// writes (FinalTarget/HP/Shield). Everything else — ownership, docking, the
// access flag — is Save-only, which is why the paths that change those do NOT
// use this helper: they roll their RAM change back and report the failure
// (docking.go, ship_access.go) or refuse to apply it at all (changeShipOwner).
func (w *Worker) immediateSave(ship *domain.Ship) {
	if err := w.saveShip(*ship); err != nil {
		w.logger.Error("immediate save failed",
			"err", err, "ship", int64(ship.ID), "sector", int64(ship.SectorID))
	}
}

// immediateSaveEquipment persists a ship's Equipment + folded stat columns +
// the shield_generator_destroyed marker through the equipment path (unlike
// immediateSave, which writes only dynamic fields). Used after a combat
// knockoff (TASK-100.3.9.1) so the module loss and shield collapse survive
// cold-start. Same best-effort logging convention as immediateSave.
func (w *Worker) immediateSaveEquipment(ship *domain.Ship) {
	if w.repo == nil {
		return
	}
	if err := w.dbCall(context.Background(), func(ctx context.Context) error {
		return w.repo.SaveEquipment(ctx, *ship)
	}); err != nil {
		w.logger.Error("immediate save-equipment failed",
			"err", err, "ship", int64(ship.ID), "sector", int64(ship.SectorID))
	}
}

// flushAll persists the full live state of every owned ship on graceful
// shutdown. Phase 3.19 (approach B) stopped writing position/velocity/
// direction/target in the periodic BatchUpdate, so this is the only path
// that ends a clean run with fresh coordinates in the DB.
//
// It saves EVERY ship, not just the dirty set: a ship that arrived and
// stopped (or never moved this snapshot window) is not dirty, yet its DB
// position is whatever the last immediate-event Save wrote — potentially
// stale. The graceful checkpoint must capture all of them.
//
// The parent context is already cancelled by the time Run reaches here, so
// we derive a fresh bounded context from cfg.ShutdownTimeout — the same
// pattern app.go uses for the HTTP server's shutdown. Save errors are
// logged but never block the exit; a failed flush just means the next cold
// start reads slightly older coordinates.
func (w *Worker) flushAll() {
	if w.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.ShutdownTimeout)
	defer cancel()
	for _, s := range w.sectors {
		for _, ship := range s.ships {
			if err := w.repo.Save(ctx, *ship); err != nil {
				w.logger.ErrorContext(ctx, "shutdown flush save failed",
					"err", err, "sector", int64(s.sectorID), "ship", int64(ship.ID))
			}
		}
		w.flushDrones(ctx, s)
		w.flushTorpedos(ctx, s)
		w.flushAsteroids(ctx, s)
		w.flushAIState(ctx, s)
	}
}

// flushDrones writes the live drone state of one sector in a single
// BatchUpdate on graceful shutdown, so a clean run ends with fresh drone
// coordinates/HP in the DB (the periodic batch only fires on the snapshot
// interval). No-op when drone persistence is disabled or the sector has
// no live drones.
func (w *Worker) flushDrones(ctx context.Context, s *sectorState) {
	if w.droneRepo == nil || len(s.drones) == 0 {
		return
	}
	ds := make([]domain.Drone, 0, len(s.drones))
	for _, d := range s.drones {
		ds = append(ds, *d)
	}
	if err := w.droneRepo.BatchUpdate(ctx, ds); err != nil {
		w.logger.ErrorContext(ctx, "shutdown flush drones failed",
			"err", err, "sector", int64(s.sectorID), "count", len(ds))
	}
}

// flushTorpedos writes the live torpedo state of one sector in a single
// BatchUpdate on graceful shutdown, so a clean run ends with fresh torpedo
// coordinates/HP in the DB (the periodic batch only fires on the snapshot
// interval). No-op when torpedo persistence is disabled or the sector has no
// live torpedoes. Mirrors flushDrones.
func (w *Worker) flushTorpedos(ctx context.Context, s *sectorState) {
	if w.torpedoRepo == nil || len(s.torpedos) == 0 {
		return
	}
	ts := make([]domain.Torpedo, 0, len(s.torpedos))
	for _, t := range s.torpedos {
		ts = append(ts, *t)
	}
	if err := w.torpedoRepo.BatchUpdate(ctx, ts); err != nil {
		w.logger.ErrorContext(ctx, "shutdown flush torpedos failed",
			"err", err, "sector", int64(s.sectorID), "count", len(ts))
	}
}

// flushAsteroids writes the remaining mass of every live asteroid of one
// sector in a single BatchUpdate on graceful shutdown, so a clean run ends
// with fresh mass in the DB (the periodic batch only fires on the snapshot
// interval). No-op when asteroid persistence is disabled or the sector has
// no asteroids.
func (w *Worker) flushAsteroids(ctx context.Context, s *sectorState) {
	if w.asteroidRepo == nil || len(s.asteroids) == 0 {
		return
	}
	as := make([]domain.Asteroid, 0, len(s.asteroids))
	for _, a := range s.asteroids {
		as = append(as, *a)
	}
	if err := w.asteroidRepo.BatchUpdate(ctx, as); err != nil {
		w.logger.ErrorContext(ctx, "shutdown flush asteroids failed",
			"err", err, "sector", int64(s.sectorID), "count", len(as))
	}
}

// EnsureSubscriptions wires bus intake subscriptions for every owned sector
// exactly once. Run calls it as its first action, so production code never
// needs to call this directly; tests use it to synchronously establish the
// subscription before publishing into the bus (otherwise the publish can
// race the Run goroutine and be dropped).
func (w *Worker) EnsureSubscriptions(ctx context.Context) {
	if w.bus == nil {
		return
	}
	w.intakeSubOnce.Do(func() {
		w.subscribeIntake(ctx)
	})
}

// subscribeIntake registers an intake handler per owned sector. Each handler
// runs in its own bus goroutine and forwards the decoded JumpEvent to the
// worker's inbox — the one-writer-per-sector invariant is preserved because
// the actual mutation happens during the next tick.
func (w *Worker) subscribeIntake(ctx context.Context) {
	for sectorID := range w.sectors {
		sid := sectorID
		err := w.bus.Subscribe(ctx, IntakeTopic(sid), func(payload []byte) {
			var ev JumpEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				w.logger.ErrorContext(ctx, "decode jump event",
					"err", err, "sector", int64(sid))
				return
			}
			if err := w.Send(sid, JumpIntakeCommand{Event: ev}); err != nil {
				w.logger.ErrorContext(ctx, "enqueue jump intake",
					"err", err, "sector", int64(sid), "ship", int64(ev.Ship.ID))
			}
		})
		if err != nil {
			w.logger.ErrorContext(ctx, "subscribe intake",
				"err", err, "sector", int64(sid))
		}
	}
}

// Tick runs one cycle for every owned sector: drains queued commands,
// advances movement by TickInterval, flushes the periodic snapshot if its
// interval elapsed, broadcasts patches, and publishes a new Snapshot. Not
// safe to call concurrently with Run.
func (w *Worker) Tick(ctx context.Context) {
	started := w.clock.Now()
	w.metrics.SetQueueDepth(w.idx, len(w.inbox))
	w.drainInbox()
	dt := w.cfg.TickInterval.Seconds()
	for _, state := range w.sectors {
		w.tickSector(ctx, state, dt)
	}
	if elapsed := w.clock.Now().Sub(started); elapsed > w.cfg.TickInterval {
		w.overruns.Add(1)
		w.metrics.IncTickOverrun(w.idx)
		w.logger.Warn("tick overrun",
			"elapsed", elapsed, "interval", w.cfg.TickInterval, "sectors", len(w.sectors))
	}
}

func (w *Worker) tickSector(ctx context.Context, s *sectorState, baseDt float64) {
	started := w.clock.Now()
	// TiDi (7.2): scale game time by the sector's dilation factor. timeScale
	// (set at the end of the previous tick from its compute load) slows the
	// integrated motion of ships/missiles/drones under overload, keeping TPS
	// up instead of dropping ticks. Per-tick steps (shields/energy charge,
	// reactive AI) are unaffected. timeScale == 1.0 is a no-op.
	dt := baseDt * s.timeScale
	w.tickAI(ctx, s)
	w.tickPlayerMining(ctx, s)
	resolveAutopilot(s, w.router, w.cfg.DockRange)
	applyMovement(s, dt)
	carryDockedShips(s)
	w.tickExternalDock(s)
	w.tryAutoJump(s)
	w.tryAutoDock(s)
	chargeShields(s)
	chargeStatics(s)
	chargeEnergies(s)
	w.fireLasers(ctx, s)
	w.tickPoliceScan(ctx, s)
	w.tickTowers(ctx, s)
	w.tickMissiles(ctx, s, dt, started)
	w.tickDrones(ctx, s, dt, started)
	w.tickTorpedos(ctx, s, dt, started)
	w.sweepKilledShips(ctx, s)
	w.tickContainers(ctx, s, started)
	w.runProduction(ctx, s, started)
	w.persistDirty(ctx, s)
	w.persistDirtyDrones(ctx, s)
	w.persistDirtyTorpedos(ctx, s)
	w.persistAsteroids(ctx, s)
	w.persistAIState(ctx, s)
	s.tick++
	snapStarted := w.clock.Now()
	broadcastPatches(w.logger, s, w.cfg.AOIRadius*aoiCellFactor, aoiParams{
		fallbackRadius:  w.cfg.AOIRadius,
		stealthDetect:   w.cfg.StealthDetectRange,
		relations:       w.relations,
		satelliteReveal: w.cfg.SatelliteRevealRadius,
	})
	elapsed := snapStarted.Sub(started)
	s.lastDuration = elapsed
	publishSnapshotFor(s, elapsed)
	snapElapsed := w.clock.Now().Sub(snapStarted)
	s.clearLaserEffects()
	s.clearMissileImpacts()
	s.clearDroneImpacts()
	s.clearTorpedoImpacts()
	s.clearStaticCombatDeltas()
	// Clear one-tick stealth reveals (phase 10.20a) so they apply for
	// exactly the snapshot where the missile was fired, not subsequent ticks.
	for _, ship := range s.ships {
		ship.MissileJustFired = false
	}
	// TiDi: recompute the dilation factor from this tick's total compute time
	// for the next tick. Warn when it drops into the degraded band.
	totalDur := elapsed + snapElapsed
	prevScale := s.timeScale
	s.timeScale = adjustTimeScale(prevScale, totalDur, w.cfg.TickInterval)
	if s.timeScale < timeScaleWarnThreshold && s.timeScale < prevScale {
		w.logger.Warn("sector time dilation",
			"sector", int64(s.sectorID), "time_scale", s.timeScale, "tick_dur", totalDur)
	}
	w.metrics.RecordTick(s.sectorID, totalDur, snapElapsed, len(s.ships), len(s.dirty), s.timeScale)
}

// runProduction advances every station's production cycle in s. The
// ticker mutates s.statics.Stations in place; on error we log and keep
// the tick alive — production must not stall the whole sector.
func (w *Worker) runProduction(ctx context.Context, s *sectorState, now time.Time) {
	if w.production == nil || len(s.statics.Stations) == 0 {
		return
	}
	// production.Service.Tick is a LOOP of one transaction per station, not one
	// round trip, so cfg.RepoTimeout bounds the whole cycle. Against a degraded
	// (not hung) Postgres the cycle is truncated, and with a fixed starting point
	// it would be truncated at the same place every tick — the tail of the slice
	// would simply stop producing, deterministically, while the head kept going.
	// Rotating the entry point by the tick counter spreads the truncation evenly:
	// the two calls share one deadline (so the bound is unchanged) and both
	// sub-slices alias the same backing array, so the in-place mutation the
	// ticker performs still lands on the sector's own stations.
	stations := s.statics.Stations
	off := int(s.tick % uint64(len(stations)))
	var cycles int
	err := w.dbCall(ctx, func(ctx context.Context) error {
		head, errHead := w.production.Tick(ctx, stations[off:], now)
		tail, errTail := w.production.Tick(ctx, stations[:off], now)
		cycles = head + tail
		return errors.Join(errHead, errTail)
	})
	if err != nil {
		if dbDeadline(err) {
			w.logger.WarnContext(ctx, "production cycle truncated by the repo deadline",
				"err", err, "sector", int64(s.sectorID), "stations", len(stations),
				"start", off, "repo_timeout", w.cfg.RepoTimeout)
		} else {
			w.logger.ErrorContext(ctx, "production tick", "err", err, "sector", int64(s.sectorID))
		}
	}
	if cycles > 0 {
		s.productionCycles += uint64(cycles)
	}
}

// nextSubID is single-threaded under the tick goroutine (drainInbox runs
// commands one at a time), so it does not need to be atomic.
func (w *Worker) nextSubID() uint64 {
	w.subIDSeq++
	return w.subIDSeq
}

// drainInbox consumes every queued envelope and applies its command to the
// matching sectorState. Envelopes for unknown sectors are dropped (Send is
// the only producer and it validates the sector id beforehand, so the only
// way to land here is a race with sector ownership changes — not supported
// yet, but we don't crash).
//
// The drain carries a dbBudget (see the Worker field): the commands that write
// synchronously charge their DB time against it and the drain stops once it is
// spent, leaving the remaining envelopes queued for the next tick / wake-up.
func (w *Worker) drainInbox() {
	w.dbBudget = w.cfg.RepoTimeout
	w.drainQueued()
}

// applyAndDrain runs one envelope that already woke Run up and then drains
// whatever else is queued, under a single shared dbBudget — the woken envelope
// may itself be one of the writing commands, so it must be inside the budget
// rather than ahead of it.
func (w *Worker) applyAndDrain(env envelope) {
	w.dbBudget = w.cfg.RepoTimeout
	w.applyEnvelope(env)
	w.drainQueued()
}

// drainQueued is the drain loop proper: it applies queued envelopes and stops as
// soon as the current drain's DB budget is spent. A spent budget means the DB is
// dragging (or hung), so we return to Run rather than pay another RepoTimeout for
// the next queued writer without the ticker getting a look in.
//
// Returning does not reduce the total stall: Run may take the very next envelope
// out of the inbox and pay again (applyAndDrain starts a fresh budget). What it
// buys is that the tick is served between those payments — see the dbBudget
// field for what is and is not bounded, and TASK-148 for bounding the total.
//
// The budget is checked AFTER applying, never before: it decides whether to
// continue, so every drain makes progress on at least one command no matter how
// small (or zero) RepoTimeout is.
func (w *Worker) drainQueued() {
	for {
		select {
		case env := <-w.inbox:
			w.applyEnvelope(env)
		default:
			return
		}
		if w.dbBudget <= 0 {
			// Only worth reporting when something is actually left behind: the
			// budget can be spent by the last command in the queue, and a
			// "drain stopped, queued=0" line would claim a backlog that is not
			// there.
			if queued := len(w.inbox); queued > 0 {
				w.logger.Warn("inbox drain stopped on db budget",
					"queued", queued, "repo_timeout", w.cfg.RepoTimeout)
			}
			return
		}
	}
}

// spendDBBudget charges the current drain for a synchronous DB call that began
// at started. Since TASK-148 every DB call in the package charges it, through
// dbCall — a command that does no I/O still costs the budget nothing, because
// nothing calls this on its behalf.
//
// It takes the start instant rather than a duration so that measuring the call
// is dbCallCost's job alone: every helper charges through it and none can drift
// onto a clock of its own.
func (w *Worker) spendDBBudget(started time.Time) {
	w.dbBudget -= w.dbCallCost(started)
}

// dbCallCost measures what a synchronous DB call that began at started cost the
// drain. Wall clock on purpose — the budget bounds real time parked on DB I/O,
// which the injected clock does not model: it can be frozen, fake or scaled,
// and charging the budget against it would let a hung Postgres cost the drain
// nothing (see TestUnit_Worker_DBBudgetChargesRealElapsedTime).
//
// The measurement lives here rather than as a NewWorker default so that an
// unset dbSince cannot mean "cost nothing": the field is opt-in for tests
// (WithDBDurationSource), and a Worker built any other way — including the
// hand-built literals white-box tests use — measures real time.
func (w *Worker) dbCallCost(started time.Time) time.Duration {
	if w.dbSince == nil {
		return time.Since(started)
	}
	return w.dbSince(started)
}

// dbCall runs one synchronous DB (or bus) call from the Run goroutine and is
// the only sanctioned way to make one anywhere in this package (TASK-148). It
// does two things, and both must happen for every such call:
//
//   - bounds it by cfg.RepoTimeout. TASK-144/147/152 did this for the install,
//     launch and recall commands only; everything else — saves, deletes,
//     pickups, hauls, robs, publishes, the periodic batches — still ran under a
//     bare context.Background() or an unbounded tick context, so a hung Postgres
//     parked the Run goroutine, and with it every sector this worker owns,
//     through whichever command or tick step happened to touch the DB first. The
//     tick has no context of its own to inherit a deadline from, so the deadline
//     has to be attached here.
//   - charges the call's real cost to the current drain's DB budget, so a queue
//     of DB-touching commands cannot chain one RepoTimeout stall per command with
//     no tick in between (see the dbBudget field).
//
// parent is context.Background() for the command-apply path (a command carries
// no context) and the tick's own context for the tick path, so a shutdown still
// cancels a tick-driven call immediately instead of waiting out the deadline.
//
// The budget is charged from the tick path too, even though only drainQueued
// reads it: drainInbox resets it at the top of every Tick, so a tick's own
// spending is discarded before the next drain and cannot starve it. Charging
// unconditionally keeps one code path here rather than a "does this site count?"
// decision at forty call sites.
//
// What this does NOT bound is one tick's total DB time: a tick that kills ships,
// mines, publishes and then flushes pays up to RepoTimeout per call. The bound is
// per call and therefore proportional to how many DB operations a tick performs
// — finite and self-limiting, unlike the previous "forever".
func (w *Worker) dbCall(parent context.Context, fn func(ctx context.Context) error) error {
	timeout := w.cfg.RepoTimeout
	if timeout <= 0 {
		// A non-positive timeout is an already-blown deadline: every call would
		// fail with DeadlineExceeded before touching the DB. withDefaults keeps
		// production away from that, but the white-box tests build Worker literals
		// directly and would otherwise run their stubs under a dead context.
		timeout = defaultRepoTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	started := time.Now()
	err := fn(ctx)
	w.spendDBBudget(started)
	return err
}

// publish emits one NOTIFICATION bus message under the same contract as a DB
// call (TASK-148), and for the same reason rather than out of symmetry:
// bus.InMemory.Publish blocks once a subscriber is more than SubscriberBuffer
// messages behind — it takes back-pressure over silent loss deliberately — and
// under context.Background() that block had no end. A worker whose intake
// subscriber stopped draining (its own Run goroutine already parked on a hung DB,
// say) could therefore park the publishing worker too, which is how a single
// stuck sector becomes two.
//
// Multi-subscriber semantics, which are the whole reason publishEffect exists
// alongside this: a deadline here can deliver to SOME subscribers of a topic and
// not others (bus.PartialDelivery names which). That is acceptable only where a
// missed delivery costs the client a refresh — WS re-binds, journal lines,
// police scans, module knockoffs, the docked/jumped pacer triggers, and the
// single-subscriber sector intake. It is NOT acceptable where the subscriber
// performs an irreversible effect nobody will redo; those topics go through
// publishEffect.
//
// Callers must have checked w.bus != nil; every publish site already gates on it
// to stay a no-op in pure unit tests.
func (w *Worker) publish(parent context.Context, topic string, payload []byte) error {
	return w.dbCall(parent, func(ctx context.Context) error {
		return w.bus.Publish(ctx, topic, payload)
	})
}

// publishEffect emits a bus message whose DELIVERY IS THE STATE CHANGE, and is
// therefore the one path in this package that deliberately carries no deadline
// (TASK-148 review). Two topics qualify:
//
//   - EntityKilledTopic — four subscribers in app.go, each doing irreversible DB
//     work: bounty payout, insurance payout, quest progress, and the spacesuit
//     respawn that is the ONLY thing standing between a dead player and an
//     account with no ship at all. The victim's row is already gone (RecordKill)
//     by the time this runs.
//   - ShipCapturedTopic — ejects the captured ship's old crew into spacesuits.
//     Same "the ship is already re-owned" one-way street.
//
// Nothing retries these. There is no outbox, and the publisher cannot tell a
// subscriber "you missed one". So the only correct failure mode is the one this
// bus was built with: block until every subscriber has taken it (back-pressure),
// and let the tick pay for it. A bounded publish here would trade a recoverable
// stall for permanently broken player state — which is exactly the regression
// TASK-148's first cut introduced, and why these two topics are carved out
// rather than sharing publish's deadline.
//
// The stall is still bounded in practice by the subscribers' own progress: with
// bus.InMemory's fixed per-subscriber buffer, this blocks only once a handler is
// SubscriberBuffer events behind. The real fix — a transactional outbox, so the
// effect is durable before the tick moves on — is a separate change.
func (w *Worker) publishEffect(topic string, payload []byte) error {
	started := time.Now()
	err := w.bus.Publish(context.Background(), topic, payload)
	w.spendDBBudget(started)
	return err
}

// dbDeadline reports whether err is the in-tick deadline (or a shutdown
// cancellation) firing rather than the DB answering. It is the "outcome in
// doubt" test: cfg.RepoTimeout can expire while COMMIT is already in flight, in
// which case pgx tears the connection down and reports DeadlineExceeded while
// Postgres commits anyway. Every other error means the transaction rolled back
// and nothing happened. Callers use it to pick ERROR (in doubt, needs hand
// reconciliation) over WARN (clean failure), the same split logInstallError and
// logOrdnanceError spell out inline.
func dbDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// logInstallError records a failed jammer/satellite install (TASK-144). A
// deadline/cancellation is logged at ERROR with everything needed to reconcile
// it by hand, because it is the one outcome the atomicity invariant cannot
// cover: if cfg.RepoTimeout fires while COMMIT is already in flight, pgx tears
// the connection down and reports DeadlineExceeded while Postgres commits
// anyway. The goods are then gone and the row exists with built=true, but
// addJammer/addSatellite never ran — the object is missing from RAM (jamming
// nothing, invisible to clients) until a restart's LoadAll picks it up. Every
// other error means the transaction rolled back and nothing happened, so it is
// logged at WARN.
func (w *Worker) logInstallError(err error, kind string, ship *domain.Ship, gtype domain.GoodsTypeID) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		w.logger.Error("static install outcome in doubt: goods may be charged for an object missing from RAM until restart",
			"err", err, "object", kind, "ship", int64(ship.ID), "player", int64(ship.PlayerID),
			"sector", int64(ship.SectorID), "goods_type", int64(gtype),
			"repo_timeout", w.cfg.RepoTimeout)
		return
	}
	w.logger.Warn("static install failed",
		"err", err, "object", kind, "ship", int64(ship.ID), "player", int64(ship.PlayerID),
		"sector", int64(ship.SectorID), "goods_type", int64(gtype))
}

// applyEnvelope routes one queued command to its sector. Envelopes for unknown
// sectors are dropped (Send validates the sector id, so the only way here is a
// race with sector-ownership changes — not supported yet, but we don't crash).
// Called only from the Run goroutine (the inbox-wake case and drainInbox), so
// the one-writer-per-sector invariant holds.
func (w *Worker) applyEnvelope(env envelope) {
	s := w.sectors[env.sectorID]
	if s == nil {
		w.logger.Warn("dropping command for unknown sector", "sector", int64(env.sectorID))
		return
	}
	env.cmd.apply(w, s)
}

// publishSnapshotFor swaps in a fresh Snapshot value for the sector. Called
// at the end of each per-sector tick. The Snapshot's Ships slice is
// independent of worker state, so consumers may mutate it freely.
//
// Every per-tick effect buffer — LaserEffects, MissileImpacts, DroneImpacts,
// TorpedoImpacts — is copied, not aliased, so the worker can clear its own
// slices on the next tick without invalidating subscribers. The clear
// truncates with [:0] and the next impact appends back into the same backing
// array, so an aliased snapshot would be silently rewritten under its holder.
// Missiles, Drones and Torpedos follow the same isolation contract.
func publishSnapshotFor(s *sectorState, elapsed time.Duration) {
	out, in := s.handoffCopies()
	var effects []combat.LaserBeam
	if len(s.laserEffects) > 0 {
		effects = make([]combat.LaserBeam, len(s.laserEffects))
		copy(effects, s.laserEffects)
	}
	var impacts []MissileImpact
	if len(s.missileImpacts) > 0 {
		impacts = make([]MissileImpact, len(s.missileImpacts))
		copy(impacts, s.missileImpacts)
	}
	var dImpacts []DroneImpact
	if len(s.droneImpacts) > 0 {
		dImpacts = make([]DroneImpact, len(s.droneImpacts))
		copy(dImpacts, s.droneImpacts)
	}
	var tImpacts []TorpedoImpact
	if len(s.torpedoImpacts) > 0 {
		tImpacts = make([]TorpedoImpact, len(s.torpedoImpacts))
		copy(tImpacts, s.torpedoImpacts)
	}
	snap := &Snapshot{
		SectorID:         s.sectorID,
		Tick:             s.tick,
		Ships:            snapshotShips(s.ships),
		Statics:          cloneStatics(s.statics),
		LastTickDuration: elapsed,
		HandoffsOut:      out,
		HandoffsIn:       in,
		ProductionCycles: s.productionCycles,
		LaserEffects:     effects,
		Missiles:         snapshotMissiles(s.missiles),
		MissileImpacts:   impacts,
		Drones:           snapshotDrones(s.drones),
		DroneImpacts:     dImpacts,
		Torpedos:         snapshotTorpedos(s.torpedos),
		TorpedoImpacts:   tImpacts,
		Containers:       snapshotContainers(s.containers),
		Asteroids:        s.snapshotAsteroids(),
		Destructibles:    s.snapshotDestructibles(),
	}
	s.snap.Store(snap)
}

// snapshotMissiles returns a sorted-by-ID slice of value-type missiles.
// Missile has no pointer fields, so a plain value copy is enough for
// the worker→subscriber isolation contract.
func snapshotMissiles(src map[domain.MissileID]*domain.Missile) []domain.Missile {
	if len(src) == 0 {
		return nil
	}
	out := make([]domain.Missile, 0, len(src))
	for _, m := range src {
		out = append(out, *m)
	}
	sortMissiles(out)
	return out
}

// cloneStatics returns a deep copy of statics so the snapshot does not
// alias the worker's authoritative slices. In phase 3.1 the slices are
// effectively immutable, but the copy keeps the door open for in-place
// HP/Shield updates in later phases without spooky action on subscribers.
func cloneStatics(s domain.SectorStatics) domain.SectorStatics {
	if s.IsEmpty() {
		return domain.SectorStatics{}
	}
	out := domain.SectorStatics{}
	if len(s.Stations) > 0 {
		out.Stations = append([]domain.Station(nil), s.Stations...)
	}
	if len(s.Shipyards) > 0 {
		out.Shipyards = append([]domain.Shipyard(nil), s.Shipyards...)
	}
	if len(s.TradeStations) > 0 {
		out.TradeStations = append([]domain.TradeStation(nil), s.TradeStations...)
	}
	if len(s.Pirbases) > 0 {
		out.Pirbases = append([]domain.Pirbase(nil), s.Pirbases...)
	}
	if len(s.LaserTowers) > 0 {
		out.LaserTowers = append([]domain.LaserTower(nil), s.LaserTowers...)
	}
	if len(s.Satellites) > 0 {
		out.Satellites = append([]domain.Satellite(nil), s.Satellites...)
	}
	if len(s.Jammers) > 0 {
		out.Jammers = append([]domain.Jammer(nil), s.Jammers...)
	}
	return out
}

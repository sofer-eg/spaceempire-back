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
	BatchUpdate(ctx context.Context, ships []domain.Ship) error
	Delete(ctx context.Context, id domain.ShipID) error
}

// DroneRepo is the persistence surface for combat drones (phase 4.4).
// The real implementation lives in internal/persistence/drones. Wired in
// via WithDrones; nil disables drone persistence — drones still launch
// and fly but are not restored after a restart (used by unit tests).
type DroneRepo interface {
	Create(ctx context.Context, d domain.Drone) (domain.DroneID, error)
	BatchUpdate(ctx context.Context, ds []domain.Drone) error
	Delete(ctx context.Context, id domain.DroneID) error
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

// SatelliteRepo persists player-deployed navigation satellites (phase 10.15):
// Create on install (DB-assigned id), Delete on destruction so a restart does
// not resurrect a killed satellite. Nil makes installs RAM-only (a fallback id
// counter is used) — handy for pure unit tests. Wired via WithSatellites.
type SatelliteRepo interface {
	Create(ctx context.Context, s domain.Satellite) (domain.SatelliteID, error)
	Delete(ctx context.Context, id domain.SatelliteID) error
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

// Relations is the worker's hostility oracle (phase 6.2a): ship-vs-ship
// standing for laser friendly-fire gating and drone auto-acquire. Injected
// via WithRelations; nil keeps the pre-6.2a behaviour (lasers fire at any
// set target, drones never auto-acquire) so pure unit tests need no wiring.
// Satisfied by *relations.Service.
type Relations interface {
	IsHostile(a, b domain.EntityRef) bool
	Get(a, b domain.EntityRef) domain.Relation
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

	// containerRepo persists loot containers (phase 4.6). Nil disables
	// persistence (ships still die, but no container drops). Wired via
	// WithContainers together with initialContainers.
	containerRepo ContainerRepo

	// towerRepo persists laser-tower destruction (phase 8.5). Nil disables it
	// (a killed tower is restored on restart). Wired via WithTowerPersistence.
	towerRepo TowerRepo

	// satelliteRepo persists navigation-satellite install/destruction (phase
	// 10.15). Nil makes installs RAM-only (fallback id counter). Wired via
	// WithSatellites.
	satelliteRepo SatelliteRepo

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

// WithSatellites enables player-deployed navigation satellites (phase 10.15):
// the install command persists each new satellite (Create) and killStatic
// deletes a destroyed one. A nil repo keeps installs RAM-only (fallback id
// counter), used by pure unit tests.
func WithSatellites(repo SatelliteRepo) Option {
	return func(w *Worker) {
		w.satelliteRepo = repo
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
		w.sectors[id] = newSectorState(id, ships, w.initialDrones[id], w.initialContainers[id], w.initialAsteroids[id], w.initialStatics[id], now)
	}
	w.initialStatics = nil
	w.initialDrones = nil
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
			w.applyEnvelope(env)
			w.drainInbox()
		}
	}
}

// immediateSave wraps the optional repo.Save with the same logging
// convention used by flushAll: a save failure is logged but never
// propagated to the caller — at worst the next periodic BatchUpdate
// (or the shutdown flush) carries the change. Used for command-driven
// immediate writes (AttackCommand, CeaseFireCommand) where the player
// has already moved on to the next click by the time we return.
func (w *Worker) immediateSave(ship *domain.Ship) {
	if w.repo == nil {
		return
	}
	if err := w.repo.Save(context.Background(), *ship); err != nil {
		w.logger.Error("immediate save failed",
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
	w.tryAutoJump(s)
	chargeShields(s)
	chargeStatics(s)
	chargeEnergies(s)
	w.fireLasers(ctx, s)
	w.tickPoliceScan(ctx, s)
	w.tickTowers(s)
	tickMissiles(s, dt, started)
	w.tickDrones(ctx, s, dt, started)
	w.sweepKilledShips(ctx, s)
	w.tickContainers(ctx, s, started)
	w.runProduction(ctx, s, started)
	w.persistDirty(ctx, s)
	w.persistDirtyDrones(ctx, s)
	w.persistAsteroids(ctx, s)
	w.persistAIState(ctx, s)
	s.tick++
	snapStarted := w.clock.Now()
	broadcastPatches(w.logger, s, w.cfg.AOIRadius*aoiCellFactor, aoiParams{
		fallbackRadius:  w.cfg.AOIRadius,
		bigMult:         w.cfg.RadarBigMultiplier,
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
	cycles, err := w.production.Tick(ctx, s.statics.Stations, now)
	if err != nil {
		w.logger.ErrorContext(ctx, "production tick", "err", err, "sector", int64(s.sectorID))
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
func (w *Worker) drainInbox() {
	for {
		select {
		case env := <-w.inbox:
			w.applyEnvelope(env)
		default:
			return
		}
	}
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
// LaserEffects are copied (not aliased) so the worker can clear its own
// slice on the next tick without invalidating subscribers. Missiles and
// MissileImpacts follow the same isolation contract.
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
		Containers:       snapshotContainers(s.containers),
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
	if len(s.Satellites) > 0 {
		out.Satellites = append([]domain.Satellite(nil), s.Satellites...)
	}
	return out
}

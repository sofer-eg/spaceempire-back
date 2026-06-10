package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// Invasion faction races (8.13): Xenon machines break through gates, Kha'ak
// clusters jump in. Both are hostile to everyone by the default matrix, so the
// spawned race controllers engage players and navy alike.
const (
	xenonRace = domain.RaceID(7)
	khaakRace = domain.RaceID(8)
)

// invasionSpawnRing spreads a freshly spawned wave onto a small ring so the
// ships do not stack on one pixel (same idea as nudgeDroneSpawn).
const invasionSpawnRing = 15.0

// InvasionConfig knobs the dynamic invasion (phase 9.5). Zero fields fall back
// to defaults.
type InvasionConfig struct {
	// Interval is the spawner cadence (the old invasion_rate): how often a new
	// wave may appear. Default 5m.
	Interval time.Duration
	// XenonMaxActive / KhaakMaxActive cap the live population per faction. A
	// wiped wave deletes its ship rows, dropping the count and freeing the cap.
	XenonMaxActive int
	KhaakMaxActive int
	// XenonGroupSize / KhaakGroupSize size one wave (clamped to remaining room).
	XenonGroupSize int
	KhaakGroupSize int
	// AckTimeout bounds the wait for a worker to mirror an injected ship.
	AckTimeout time.Duration
}

func (c InvasionConfig) withDefaults() InvasionConfig {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.XenonMaxActive <= 0 {
		c.XenonMaxActive = 12
	}
	if c.KhaakMaxActive <= 0 {
		c.KhaakMaxActive = 8
	}
	if c.XenonGroupSize <= 0 {
		c.XenonGroupSize = 3
	}
	if c.KhaakGroupSize <= 0 {
		c.KhaakGroupSize = 4
	}
	if c.AckTimeout <= 0 {
		c.AckTimeout = time.Second
	}
	return c
}

// invasion dependency interfaces (ISP) — *shipsrepo.Repository,
// *aistaterepo.Repository and *sector.Pool satisfy them; tests inject fakes.
type (
	shipCreator interface {
		Create(ctx context.Context, s domain.Ship) (domain.ShipID, error)
	}
	raceCounter interface {
		CountByRace(ctx context.Context) (map[domain.RaceID]int, error)
	}
	aiStateWriter interface {
		BatchUpsert(ctx context.Context, states []domain.AIState) error
	}
	commandSender interface {
		Send(sectorID domain.SectorID, cmd sector.Command) error
	}
)

// invasionPoint is a candidate spawn location: a sector and a position inside
// it (a gate exit for Xenon, a jump-in point for Kha'ak).
type invasionPoint struct {
	sector domain.SectorID
	pos    domain.Vec2
}

// invasionSpawner periodically injects Xenon/Kha'ak waves into the live world
// (phase 9.5). It persists each ship (ships row + race ai_state — no npc_ships
// row: invasion ships have no home station and the active count comes from
// ships.race) then sends an AddShipCommand so the target sector's worker
// mirrors it and hydrates its controller. A restart reloads them like any NPC.
type invasionSpawner struct {
	ships   shipCreator
	counter raceCounter
	aiState aiStateWriter
	sender  commandSender
	shipCfg ShipSpawnerConfig
	cfg     InvasionConfig
	npc     domain.PlayerID
	clock   clock.Clock
	logger  *slog.Logger
	rng     *rand.Rand

	xenonGates   []invasionPoint
	khaakPoints  []invasionPoint
	xenonClasses []balance.ShipClass
	khaakClasses []balance.ShipClass
}

// newInvasionSpawner derives the candidate spawn locations from the topology +
// static layout (Xenon at gate exits into populated sectors, Kha'ak at the
// centre of populated sectors) and the Xenon/Kha'ak combat classes from the
// 8.14 catalog. An empty candidate/class list disables that faction.
func newInvasionSpawner(
	ships shipCreator,
	counter raceCounter,
	aiState aiStateWriter,
	sender commandSender,
	topology *world.Topology,
	statics map[domain.SectorID]domain.SectorStatics,
	shipClasses *balance.ShipClasses,
	npc domain.PlayerID,
	shipCfg ShipSpawnerConfig,
	cfg InvasionConfig,
	clk clock.Clock,
	logger *slog.Logger,
) *invasionSpawner {
	populated := populatedSectors(statics)
	all := shipClasses.AllShipClasses()
	s := &invasionSpawner{
		ships:        ships,
		counter:      counter,
		aiState:      aiState,
		sender:       sender,
		shipCfg:      shipCfg.withDefaults(),
		cfg:          cfg.withDefaults(),
		npc:          npc,
		clock:        clk,
		logger:       logger,
		rng:          rand.New(rand.NewPCG(uint64(npc)+0x9e5, 0x5e5)), //nolint:gosec // spawn jitter, not security-sensitive
		xenonGates:   gateExitsInto(topology, populated),
		khaakPoints:  sectorCentres(populated),
		xenonClasses: combatClassesForRace(all, xenonRace, 3, 4, 5),
		khaakClasses: combatClassesForRace(all, khaakRace, 2, 3),
	}
	logger.Info("invasion spawner ready",
		"xenon_gates", len(s.xenonGates), "xenon_classes", len(s.xenonClasses),
		"khaak_points", len(s.khaakPoints), "khaak_classes", len(s.khaakClasses))
	return s
}

// Run blocks until ctx is canceled, ticking once per interval (rent.Closer
// pattern).
func (s *invasionSpawner) Run(ctx context.Context) {
	ticker := s.clock.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			s.Tick(ctx)
		}
	}
}

// Tick reads the live per-race population and spawns a Xenon and/or Kha'ak
// wave when below the cap. Exposed so tests can drive it with a controlled
// clock.
func (s *invasionSpawner) Tick(ctx context.Context) {
	counts, err := s.counter.CountByRace(ctx)
	if err != nil {
		s.logger.Error("invasion: count by race", "err", err)
		return
	}
	s.maybeSpawn(ctx, xenonRace, counts[xenonRace], s.cfg.XenonMaxActive, s.cfg.XenonGroupSize, s.xenonGates, s.xenonClasses, true)
	s.maybeSpawn(ctx, khaakRace, counts[khaakRace], s.cfg.KhaakMaxActive, s.cfg.KhaakGroupSize, s.khaakPoints, s.khaakClasses, false)
}

// maybeSpawn injects one wave of raceID at a random candidate point when the
// live population leaves room. fromGate=true anchors the controllers at the
// sector centre so a gate-spawned Xenon wave advances inward; false anchors at
// the spawn point so a jumped-in Kha'ak cluster holds station.
func (s *invasionSpawner) maybeSpawn(
	ctx context.Context,
	raceID domain.RaceID,
	active, maxActive, groupSize int,
	points []invasionPoint,
	classes []balance.ShipClass,
	fromGate bool,
) {
	if len(points) == 0 || len(classes) == 0 {
		return
	}
	room := maxActive - active
	if room <= 0 {
		return
	}
	n := groupSize
	if n > room {
		n = room
	}
	point := points[s.rng.IntN(len(points))]
	for i := 0; i < n; i++ {
		class := classes[i%len(classes)]
		pos := ringOffset(point.pos, i, n, invasionSpawnRing)
		anchor := point.pos
		if fromGate {
			anchor = domain.Vec2{} // sector centre — advance inward from the gate
		}
		if err := s.spawnInvader(ctx, raceID, class, point.sector, pos, anchor); err != nil {
			s.logger.Error("invasion: spawn", "err", err, "race", int(raceID), "sector", int64(point.sector))
			return
		}
	}
	s.logger.Info("invasion wave", "race", int(raceID), "sector", int64(point.sector), "ships", n)
}

// spawnInvader persists one invader (ship row + race ai_state) and mirrors it
// into the live sector via AddShipCommand. Owned by the system __npc__ player.
func (s *invasionSpawner) spawnInvader(ctx context.Context, raceID domain.RaceID, class balance.ShipClass, sectorID domain.SectorID, pos, anchor domain.Vec2) error {
	ship := buildWarship(s.npc, raceID, class, s.shipCfg)
	ship.SectorID = sectorID
	ship.Pos = pos
	id, err := s.ships.Create(ctx, ship)
	if err != nil {
		return fmt.Errorf("create invader: %w", err)
	}
	ship.ID = id

	stateJSON, err := race.NewInitialState(int(raceID), anchor)
	if err != nil {
		return fmt.Errorf("invader state: %w", err)
	}
	if err := s.aiState.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         id,
		SectorID:       sectorID,
		ControllerKind: race.Kind,
		StateJSON:      stateJSON,
	}}); err != nil {
		return fmt.Errorf("invader ai state: %w", err)
	}

	reply := make(chan sector.CmdResult, 1)
	if err := s.sender.Send(sectorID, sector.AddShipCommand{
		Ship:           ship,
		ControllerKind: race.Kind,
		StateJSON:      stateJSON,
		Reply:          reply,
	}); err != nil {
		return fmt.Errorf("send add invader: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.AckTimeout)
	defer cancel()
	select {
	case res := <-reply:
		if res.Err != nil && !errors.Is(res.Err, sector.ErrShipExists) {
			return fmt.Errorf("worker add invader: %w", res.Err)
		}
		return nil
	case <-waitCtx.Done():
		return errors.New("worker add invader: ack timeout")
	}
}

// populatedSectors returns the set of sectors that host at least one static
// object — the inhabited sectors worth invading.
func populatedSectors(statics map[domain.SectorID]domain.SectorStatics) map[domain.SectorID]bool {
	out := make(map[domain.SectorID]bool)
	for id, st := range statics {
		if len(st.Stations)+len(st.Shipyards)+len(st.TradeStations)+len(st.Pirbases) > 0 {
			out[id] = true
		}
	}
	return out
}

// gateExitsInto returns one invasionPoint per gate endpoint that lands in a
// populated sector — the exit position Xenon breaks through to. Sorted by
// (sector, gate id) for determinism.
func gateExitsInto(topology *world.Topology, populated map[domain.SectorID]bool) []invasionPoint {
	var out []invasionPoint
	for _, g := range topology.Gates() {
		if populated[g.SectorA] {
			out = append(out, invasionPoint{sector: g.SectorA, pos: g.PosA})
		}
		if populated[g.SectorB] {
			out = append(out, invasionPoint{sector: g.SectorB, pos: g.PosB})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].sector != out[j].sector {
			return out[i].sector < out[j].sector
		}
		if out[i].pos.X != out[j].pos.X {
			return out[i].pos.X < out[j].pos.X
		}
		return out[i].pos.Y < out[j].pos.Y
	})
	return out
}

// sectorCentres returns the centre point of each populated sector — where a
// Kha'ak cluster jumps in. Sorted by sector for determinism.
func sectorCentres(populated map[domain.SectorID]bool) []invasionPoint {
	out := make([]invasionPoint, 0, len(populated))
	for id := range populated {
		out = append(out, invasionPoint{sector: id, pos: domain.Vec2{}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sector < out[j].sector })
	return out
}

// combatClassesForRace returns the race's classes matching the wanted class
// numbers (e.g. M3/M4/M5), falling back to every class of that race when none
// match. Sorted by (class, id) for a deterministic spawn rotation.
func combatClassesForRace(all []balance.ShipClass, raceID domain.RaceID, classFilter ...int) []balance.ShipClass {
	want := make(map[int]bool, len(classFilter))
	for _, c := range classFilter {
		want[c] = true
	}
	var filtered, any []balance.ShipClass
	for _, sc := range all {
		if domain.RaceID(sc.Race) != raceID {
			continue
		}
		any = append(any, sc)
		if want[sc.Class] {
			filtered = append(filtered, sc)
		}
	}
	out := filtered
	if len(out) == 0 {
		out = any
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ringOffset spreads the i-th ship of n onto a ring of radius r around centre.
func ringOffset(center domain.Vec2, i, n int, r float64) domain.Vec2 {
	if n <= 1 {
		return center
	}
	a := 2 * math.Pi * float64(i) / float64(n)
	return center.Add(domain.Vec2{X: r * math.Cos(a), Y: r * math.Sin(a)})
}

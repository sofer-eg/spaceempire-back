package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	aistaterepo "spaceempire/back/internal/persistence/aistate"
	npcshipsrepo "spaceempire/back/internal/persistence/npcships"
	shipsrepo "spaceempire/back/internal/persistence/ships"
)

// RaceFleetConfig knobs the cold-start race warship spawn (phase 9.3): the
// Navy of races 1-5 plus the pirate fleet (6). Counts are Go literals (not
// YAML) since the data is tiny, mirroring the 8.13 race reference.
type RaceFleetConfig struct {
	// WarshipsPerRace is the total warship budget per race, ported from the old
	// race_fleet_limit (db.sql) summed per race. Races 7/8 (Xenon/Kaak) are
	// invasion-only (phase 9.5) and are not seeded here.
	WarshipsPerRace map[int]int
	// MaxPerSector caps how many warships of one race anchor in a single sector
	// — the per-sector perf ceiling (70 workers).
	MaxPerSector int
}

func (c RaceFleetConfig) withDefaults() RaceFleetConfig {
	if c.WarshipsPerRace == nil {
		// Summed FleetCount per race from race_fleet_limit (db.sql).
		c.WarshipsPerRace = map[int]int{1: 24, 2: 35, 3: 28, 4: 23, 5: 24, 6: 37}
	}
	if c.MaxPerSector <= 0 {
		c.MaxPerSector = 6
	}
	return c
}

// raceFleetMaxRace is the highest race the fleet spawner seeds: 1-5 Navy + 6
// pirates. 7 (Xenon) / 8 (Kaak) arrive via invasion (phase 9.5).
const raceFleetMaxRace = 6

// Warship combat-stat derivation from the 8.14 catalog (phase 9.3). The
// catalog Laser is a mount-power budget, not per-tick damage; scaling it down
// keeps the M6 > M3 > M4 ordering and makes warship-vs-warship combat resolve
// in tens of ticks. Energy is sized so a warship can sustain fire. Cross-scale
// balance vs the placeholder player ship is a 7.4 concern.
const (
	warshipLaserDivisor   = 10
	warshipEnergy         = 100000
	warshipEnergyRecharge = 50
)

// raceFleetSpawner seeds racial warships at cold-start: a ship row owned by the
// system player (race set), a race ai_state anchored at its station, and an
// npc_ships identity row (kind "race"). Runs before the workers load ships so
// the inserted rows are hydrated by the normal cold-start path. Idempotent —
// a sector already at quota is skipped, and because npc_ships cascades on ship
// delete, a restart tops the fleet back up to the limit.
type raceFleetSpawner struct {
	ships       *shipsrepo.Repository
	aiState     *aistaterepo.Repository
	npc         *npcshipsrepo.Repository
	shipClasses *balance.ShipClasses
	ship        ShipSpawnerConfig
	cfg         RaceFleetConfig
	logger      *slog.Logger
}

func newRaceFleetSpawner(
	ships *shipsrepo.Repository,
	aiState *aistaterepo.Repository,
	npc *npcshipsrepo.Repository,
	shipClasses *balance.ShipClasses,
	shipCfg ShipSpawnerConfig,
	cfg RaceFleetConfig,
	logger *slog.Logger,
) *raceFleetSpawner {
	return &raceFleetSpawner{
		ships:       ships,
		aiState:     aiState,
		npc:         npc,
		shipClasses: shipClasses,
		ship:        shipCfg.withDefaults(),
		cfg:         cfg.withDefaults(),
		logger:      logger,
	}
}

// EnsureSpawned tops up every race's warship fleet to its budget, spreading
// ships across the race's territory (sectors where it owns a station) and
// anchoring each at a station. statics is the per-sector static layout, already
// loaded. A persistence failure is returned; an empty plan is a no-op.
func (s *raceFleetSpawner) EnsureSpawned(ctx context.Context, statics map[domain.SectorID]domain.SectorStatics) error {
	owner, err := s.npc.SystemPlayerID(ctx)
	if err != nil {
		return fmt.Errorf("npc system player: %w", err)
	}
	served, err := s.npc.CountByHome(ctx)
	if err != nil {
		return fmt.Errorf("count npc by home: %w", err)
	}

	anchors := collectRaceAnchors(statics)
	classes := warshipClassesByRace(s.shipClasses.AllShipClasses())
	plans := planRaceFleets(anchors, classes, served, s.cfg)

	spawned := 0
	for _, p := range plans {
		if err := s.spawnWarship(ctx, owner, p); err != nil {
			return fmt.Errorf("spawn warship race %d at %v: %w", p.race, p.anchor.ref, err)
		}
		spawned++
	}
	if spawned > 0 {
		s.logger.Info("race fleet spawned at cold-start", "count", spawned)
	}
	return nil
}

// spawnWarship persists one warship: the ship row, its race ai_state anchored
// at the spawn station, and the npc_ships identity row (kind "race").
func (s *raceFleetSpawner) spawnWarship(ctx context.Context, owner domain.PlayerID, p warshipSpawn) error {
	id, err := s.ships.Create(ctx, s.newWarship(owner, p))
	if err != nil {
		return fmt.Errorf("create ship: %w", err)
	}

	stateJSON, err := race.NewInitialState(p.race, p.anchor.pos)
	if err != nil {
		return fmt.Errorf("race state: %w", err)
	}
	if err := s.aiState.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         id,
		SectorID:       p.anchor.sector,
		ControllerKind: race.Kind,
		StateJSON:      stateJSON,
	}}); err != nil {
		return fmt.Errorf("ai state: %w", err)
	}

	if err := s.npc.Create(ctx, id, p.anchor.ref, race.Kind); err != nil {
		return fmt.Errorf("npc_ships: %w", err)
	}
	return nil
}

// newWarship builds the ship row for a racial warship: race set, combat stats
// from the 8.14 class, energy/laser sized so it can fight. idx offsets the
// spawn position so co-anchored ships do not overlap exactly.
func (s *raceFleetSpawner) newWarship(owner domain.PlayerID, p warshipSpawn) domain.Ship {
	ship := buildWarship(owner, domain.RaceID(p.race), p.class, s.ship)
	ship.SectorID = p.anchor.sector
	ship.Pos = domain.Vec2{X: p.anchor.pos.X + float64(p.idx)*5, Y: p.anchor.pos.Y}
	return ship
}

// buildWarship derives a warship's combat stats from an 8.14 catalog class
// (phase 9.3), shared by the cold-start race-fleet spawner and the runtime
// invasion spawner (9.5). The caller sets SectorID and Pos afterwards — this
// returns a ship at the origin. The catalog Laser is a mount-power budget, not
// per-tick damage; scaling it down keeps the M6>M3>M4 ordering, with the
// player-ship laser as a floor so no warship is harmless.
func buildWarship(owner domain.PlayerID, race domain.RaceID, class balance.ShipClass, shipCfg ShipSpawnerConfig) domain.Ship {
	laser := class.Laser / warshipLaserDivisor
	if laser < shipCfg.StartLaserDamage {
		laser = shipCfg.StartLaserDamage
	}
	return domain.Ship{
		PlayerID:        owner,
		Race:            race,
		ShipClassID:     class.ID,
		Direction:       domain.Vec2{X: 1, Y: 0},
		MaxSpeed:        class.Speed,
		Acceleration:    class.Acceleration,
		TurnRate:        shipCfg.StartTurnRate,
		HP:              class.Hull,
		MaxHP:           class.Hull,
		Shield:          class.Shield,
		MaxShield:       class.Shield,
		ShieldRecharge:  class.ShieldCharge,
		Energy:          warshipEnergy,
		MaxEnergy:       warshipEnergy,
		EnergyRecharge:  warshipEnergyRecharge,
		LaserDamage:     laser,
		LaserRange:      shipCfg.StartLaserRange,
		LaserEnergyCost: shipCfg.StartLaserECost,
		RadarRange:      float64(class.Radar),    // phase 10.20 L1
		CargoBay:        float64(class.CargoBay), // phase 10.3.17: hold capacity from class
	}
}

// anchorRef is a patrol anchor: a built race station (or pirbase) that warships
// spawn at and patrol around.
type anchorRef struct {
	ref    domain.EntityRef
	sector domain.SectorID
	pos    domain.Vec2
}

// warshipSpawn is one planned warship: its race, anchor, class, and per-anchor
// index (drives the spawn-position offset and class rotation).
type warshipSpawn struct {
	race   int
	anchor anchorRef
	class  balance.ShipClass
	idx    int
}

// collectRaceAnchors groups built stations and pirbases by race into one anchor
// per (race, sector): the lowest-id object in that sector. Only races 1..6
// (Navy + pirates) are seeded; anchors are sorted by sector for determinism.
func collectRaceAnchors(statics map[domain.SectorID]domain.SectorStatics) map[int][]anchorRef {
	type key struct {
		race   int
		sector domain.SectorID
	}
	best := map[key]anchorRef{}
	consider := func(rc int, sector domain.SectorID, ref domain.EntityRef, pos domain.Vec2) {
		if rc < 1 || rc > raceFleetMaxRace {
			return
		}
		k := key{rc, sector}
		if cur, ok := best[k]; !ok || ref.ID < cur.ref.ID {
			best[k] = anchorRef{ref: ref, sector: sector, pos: pos}
		}
	}
	for _, st := range statics {
		for _, station := range st.Stations {
			if station.Built {
				consider(station.Race, station.SectorID, station.ObjectID(), station.Pos)
			}
		}
		for _, pb := range st.Pirbases {
			if pb.Built {
				consider(pb.Race, pb.SectorID, pb.ObjectID(), pb.Pos)
			}
		}
	}
	out := map[int][]anchorRef{}
	for k, a := range best {
		out[k.race] = append(out[k.race], a)
	}
	for rc := range out {
		anchors := out[rc]
		sort.Slice(anchors, func(i, j int) bool { return anchors[i].sector < anchors[j].sector })
	}
	return out
}

// warshipClassesByRace returns each race's combat classes (M3/M4/M6 from the
// 8.14 catalog), sorted by (class, id) for a deterministic rotation. A race
// with no such class gets no entry and the planner skips it.
func warshipClassesByRace(all []balance.ShipClass) map[int][]balance.ShipClass {
	out := map[int][]balance.ShipClass{}
	for _, sc := range all {
		switch sc.Class {
		case 3, 4, 6: // M3 heavy fighter, M4 fighter, M6 corvette
			out[sc.Race] = append(out[sc.Race], sc)
		}
	}
	for rc := range out {
		list := out[rc]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Class != list[j].Class {
				return list[i].Class < list[j].Class
			}
			return list[i].ID < list[j].ID
		})
	}
	return out
}

// planRaceFleets distributes each race's warship budget evenly across its
// anchors (capped per anchor by MaxPerSector), subtracts the already-served
// count, and returns one warshipSpawn per ship still to create. Deterministic
// and idempotent: a restart re-derives the same quotas and only tops up the
// gap left by ships that died.
func planRaceFleets(
	anchorsByRace map[int][]anchorRef,
	classesByRace map[int][]balance.ShipClass,
	served map[npcshipsrepo.HomeKind]int,
	cfg RaceFleetConfig,
) []warshipSpawn {
	var out []warshipSpawn
	races := make([]int, 0, len(anchorsByRace))
	for rc := range anchorsByRace {
		races = append(races, rc)
	}
	sort.Ints(races)

	for _, rc := range races {
		classes := classesByRace[rc]
		anchors := anchorsByRace[rc]
		budget := cfg.WarshipsPerRace[rc]
		if len(classes) == 0 || len(anchors) == 0 || budget <= 0 {
			continue
		}
		quota := distributeEven(budget, len(anchors), cfg.MaxPerSector)
		for k, a := range anchors {
			have := served[npcshipsrepo.HomeKind{Home: a.ref, Kind: race.Kind}]
			for j := have; j < quota[k]; j++ {
				out = append(out, warshipSpawn{
					race:   rc,
					anchor: a,
					class:  classes[j%len(classes)],
					idx:    j,
				})
			}
		}
	}
	return out
}

// distributeEven spreads budget across n bins, filling each evenly up to
// ceiling. The returned per-bin counts sum to min(budget, n*ceiling).
func distributeEven(budget, n, ceiling int) []int {
	q := make([]int, n)
	if n == 0 {
		return q
	}
	placed := 0
	for placed < budget {
		progressed := false
		for i := 0; i < n && placed < budget; i++ {
			if q[i] < ceiling {
				q[i]++
				placed++
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return q
}

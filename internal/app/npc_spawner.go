package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"spaceempire/back/internal/ai/miner"
	"spaceempire/back/internal/ai/passenger"
	"spaceempire/back/internal/ai/trader"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	aistaterepo "spaceempire/back/internal/persistence/aistate"
	npcshipsrepo "spaceempire/back/internal/persistence/npcships"
	shipsrepo "spaceempire/back/internal/persistence/ships"
	"spaceempire/back/internal/world"
)

// NPCSpawnerConfig knobs the cold-start NPC trader/miner/passenger spawn.
type NPCSpawnerConfig struct {
	// TradersPerFactory is how many TS traders each producing factory gets.
	TradersPerFactory int
	// MinersPerFactory is how many mining TS each ore-consuming factory gets.
	MinersPerFactory int
	// PassengersPerTradeStation is how many passenger TS each civilian (race
	// 1-4) trade station gets.
	PassengersPerTradeStation int
	// PassengerRouteRadius is the gate-hop radius from a passenger TS's home
	// within which its cruise destinations are picked.
	PassengerRouteRadius int
	// HaulQty is the per-leg cargo cap written into each trader's route.
	HaulQty int64
}

func (c NPCSpawnerConfig) withDefaults() NPCSpawnerConfig {
	if c.TradersPerFactory <= 0 {
		c.TradersPerFactory = 1
	}
	if c.MinersPerFactory <= 0 {
		c.MinersPerFactory = 1
	}
	if c.PassengersPerTradeStation <= 0 {
		c.PassengersPerTradeStation = 5
	}
	if c.PassengerRouteRadius <= 0 {
		c.PassengerRouteRadius = 2
	}
	if c.HaulQty <= 0 {
		c.HaulQty = 20
	}
	return c
}

// passengerRaceMax: passenger TS spawn on, and travel between, stations of
// civilian races 1-4 (no Teladi/pirates/Xenon/Kaak), mirroring the old
// PASSENGER_RACES_MAX.
const passengerRaceMax = 4

// tradeStationRef is a destination candidate flattened from the per-sector
// statics, used by nearestTradeStation (traders) and the passenger spawn.
type tradeStationRef struct {
	sector domain.SectorID
	pos    domain.Vec2
	ref    domain.EntityRef
	id     int64
	race   int
}

// npcSpawner creates NPC TS traders at cold-start (phase 5.3): one route per
// producing factory (home) to its nearest reachable trade station (dest).
// Idempotent — a factory already represented in npc_ships is skipped, so a
// restart does not duplicate traders. Runs before the workers load their
// ships, so the freshly inserted ship + ai_state rows are picked up by the
// normal cold-start path.
type npcSpawner struct {
	ships   *shipsrepo.Repository
	aiState *aistaterepo.Repository
	npc     *npcshipsrepo.Repository
	bal     *balance.Balance
	classes *balance.ShipClasses
	router  *world.PathRouter
	ship    ShipSpawnerConfig
	cfg     NPCSpawnerConfig
	logger  *slog.Logger
}

func newNPCSpawner(
	ships *shipsrepo.Repository,
	aiState *aistaterepo.Repository,
	npc *npcshipsrepo.Repository,
	bal *balance.Balance,
	classes *balance.ShipClasses,
	router *world.PathRouter,
	shipCfg ShipSpawnerConfig,
	cfg NPCSpawnerConfig,
	logger *slog.Logger,
) *npcSpawner {
	return &npcSpawner{
		ships:   ships,
		aiState: aiState,
		npc:     npc,
		bal:     bal,
		classes: classes,
		router:  router,
		ship:    shipCfg.withDefaults(),
		cfg:     cfg.withDefaults(),
		logger:  logger,
	}
}

// tsName returns the TS class name for the given race (e.g., race=1 → "Меркурий").
// Falls back to empty string so the client's shipDisplayName uses the race name.
func (s *npcSpawner) tsName(race int) string {
	if s.classes == nil {
		return ""
	}
	if sc, ok := s.classes.TSForRace(race); ok {
		return sc.Name
	}
	return ""
}

// tsClassID returns the race's TS class id so the spawned NPC ship renders as a
// transport silhouette on the map (phase 10.13). All three fab-ship roles
// (trader / miner / passenger) are TS-hulled. 0 when the catalog is absent or
// the race has no TS class — the client then falls back to its size heuristic.
func (s *npcSpawner) tsClassID(race int) domain.ShipClassID {
	if s.classes == nil {
		return 0
	}
	if sc, ok := s.classes.TSForRace(race); ok {
		return sc.ID
	}
	return 0
}

// EnsureSpawned spawns the missing traders, miners, and passenger TS for the
// world. statics is the per-sector static layout, asteroids the per-sector ore
// bodies (both already loaded). Returns an error only on a persistence
// failure; "nothing to serve" (no trade station / no asteroid / no passenger
// destinations) is logged and skipped, not fatal.
func (s *npcSpawner) EnsureSpawned(
	ctx context.Context,
	statics map[domain.SectorID]domain.SectorStatics,
	asteroids map[domain.SectorID][]domain.Asteroid,
) error {
	owner, err := s.npc.SystemPlayerID(ctx)
	if err != nil {
		return fmt.Errorf("npc system player: %w", err)
	}
	served, err := s.npc.CountByHome(ctx)
	if err != nil {
		return fmt.Errorf("count npc by home: %w", err)
	}

	tradeStations := collectTradeStations(statics)
	oreBodies := collectAsteroids(asteroids)
	passengerDests := collectPassengerDests(statics)

	traders, miners := 0, 0
	for _, f := range collectFactories(statics) {
		recipe, ok := s.bal.Recipe(f.Type)
		if !ok {
			continue // not a producer
		}
		n, err := s.spawnTradersFor(ctx, owner, f, recipe, tradeStations, served)
		if err != nil {
			return err
		}
		traders += n
		m, err := s.spawnMinersFor(ctx, owner, f, recipe, oreBodies, served)
		if err != nil {
			return err
		}
		miners += m
	}

	passengers := 0
	for _, ts := range tradeStations {
		if ts.race < 1 || ts.race > passengerRaceMax {
			continue // non-civilian station — no passenger service
		}
		n, err := s.spawnPassengersFor(ctx, owner, ts, passengerDests, served)
		if err != nil {
			return err
		}
		passengers += n
	}

	if traders > 0 {
		s.logger.Info("npc traders spawned at cold-start", "count", traders)
	}
	if miners > 0 {
		s.logger.Info("npc miners spawned at cold-start", "count", miners)
	}
	if passengers > 0 {
		s.logger.Info("npc passengers spawned at cold-start", "count", passengers)
	}
	return nil
}

// spawnTradersFor tops up the trader fleet of one factory: a producer (recipe
// has outputs) hauls its first output to the nearest reachable trade station.
func (s *npcSpawner) spawnTradersFor(
	ctx context.Context,
	owner domain.PlayerID,
	f domain.Station,
	recipe balance.Recipe,
	tradeStations []tradeStationRef,
	served map[npcshipsrepo.HomeKind]int,
) (int, error) {
	if len(recipe.Outputs) == 0 {
		return 0, nil
	}
	homeRef := f.ObjectID()
	have := served[npcshipsrepo.HomeKind{Home: homeRef, Kind: trader.Kind}]
	if have >= s.cfg.TradersPerFactory {
		return 0, nil
	}
	dest, ok := s.nearestTradeStation(f.SectorID, tradeStations)
	if !ok {
		s.logger.Warn("npc spawn: no reachable trade station for factory",
			"station", int64(f.ID), "sector", int64(f.SectorID))
		return 0, nil
	}

	home := trader.Leg{Sector: f.SectorID, Pos: f.Pos, Ref: homeRef}
	destLeg := trader.Leg{Sector: dest.sector, Pos: dest.pos, Ref: dest.ref}
	goods := recipe.Outputs[0].GoodsType

	spawned := 0
	for i := have; i < s.cfg.TradersPerFactory; i++ {
		if err := s.spawnTrader(ctx, owner, f, home, destLeg, goods, i); err != nil {
			return spawned, fmt.Errorf("spawn trader for station %d: %w", f.ID, err)
		}
		spawned++
	}
	s.logger.Info("npc traders assigned to factory",
		"station", int64(f.ID), "sector", int64(f.SectorID),
		"dest_sector", int64(dest.sector), "goods", int(goods))
	return spawned, nil
}

// spawnMinersFor tops up the miner fleet of one factory: a consumer (recipe
// has inputs) mines its first input good from the nearest reachable asteroid
// of that ore type.
func (s *npcSpawner) spawnMinersFor(
	ctx context.Context,
	owner domain.PlayerID,
	f domain.Station,
	recipe balance.Recipe,
	oreBodies []asteroidRef,
	served map[npcshipsrepo.HomeKind]int,
) (int, error) {
	if len(recipe.Inputs) == 0 {
		return 0, nil
	}
	ore := recipe.Inputs[0].GoodsType
	homeRef := f.ObjectID()
	have := served[npcshipsrepo.HomeKind{Home: homeRef, Kind: miner.Kind}]
	if have >= s.cfg.MinersPerFactory {
		return 0, nil
	}
	target, ok := s.nearestAsteroid(f.SectorID, ore, oreBodies)
	if !ok {
		s.logger.Warn("npc spawn: no reachable asteroid for factory",
			"station", int64(f.ID), "sector", int64(f.SectorID), "ore", int(ore))
		return 0, nil
	}

	home := miner.Leg{Sector: f.SectorID, Pos: f.Pos, Ref: homeRef}
	spawned := 0
	for i := have; i < s.cfg.MinersPerFactory; i++ {
		if err := s.spawnMiner(ctx, owner, f, home, ore, target, i); err != nil {
			return spawned, fmt.Errorf("spawn miner for station %d: %w", f.ID, err)
		}
		spawned++
	}
	s.logger.Info("npc miners assigned to factory",
		"station", int64(f.ID), "sector", int64(f.SectorID),
		"ast_sector", int64(target.Sector), "ore", int(ore))
	return spawned, nil
}

// newNPCShip builds the common ship row for an NPC fab-ship owned by the
// system player, spawned at the home object's (sector, pos). idx offsets the
// spawn position so co-located ships do not overlap exactly. race is the home
// station's faction — set on the ship so the client can colour/label it.
func (s *npcSpawner) newNPCShip(owner domain.PlayerID, sector domain.SectorID, pos domain.Vec2, race, idx int) domain.Ship {
	var radar float64 // phase 10.20 L1 — from the TS class (NPCs don't subscribe, kept for parity)
	if s.classes != nil {
		if sc, ok := s.classes.TSForRace(race); ok {
			radar = float64(sc.Radar)
		}
	}
	return domain.Ship{
		PlayerID:        owner,
		Race:            domain.RaceID(race),
		Name:            s.tsName(race),
		ShipClassID:     s.tsClassID(race),
		SectorID:        sector,
		Pos:             domain.Vec2{X: pos.X + float64(idx)*5, Y: pos.Y},
		Direction:       domain.Vec2{X: 1, Y: 0},
		MaxSpeed:        s.ship.StartMaxSpeed,
		Acceleration:    s.ship.StartAccel,
		TurnRate:        s.ship.StartTurnRate,
		HP:              s.ship.StartHP,
		MaxHP:           s.ship.StartHP,
		Shield:          s.ship.StartShld,
		MaxShield:       s.ship.StartShld,
		ShieldRecharge:  s.ship.StartShldCharge,
		Energy:          s.ship.StartEnergy,
		MaxEnergy:       s.ship.StartEnergy,
		EnergyRecharge:  s.ship.StartEnergyChrg,
		LaserDamage:     s.ship.StartLaserDamage,
		LaserRange:      s.ship.StartLaserRange,
		LaserEnergyCost: s.ship.StartLaserECost,
		RadarRange:      radar,
	}
}

// spawnTrader persists one trader: a ship row owned by the system player, its
// trader ai_state, and an npc_ships identity row (kind "trader").
func (s *npcSpawner) spawnTrader(
	ctx context.Context,
	owner domain.PlayerID,
	f domain.Station,
	home, dest trader.Leg,
	goods domain.GoodsTypeID,
	idx int,
) error {
	id, err := s.ships.Create(ctx, s.newNPCShip(owner, f.SectorID, f.Pos, f.Race, idx))
	if err != nil {
		return fmt.Errorf("create ship: %w", err)
	}

	stateJSON, err := trader.NewInitialState(home, dest, goods, s.cfg.HaulQty)
	if err != nil {
		return fmt.Errorf("trader state: %w", err)
	}
	if err := s.aiState.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         id,
		SectorID:       f.SectorID,
		ControllerKind: trader.Kind,
		StateJSON:      stateJSON,
	}}); err != nil {
		return fmt.Errorf("ai state: %w", err)
	}

	if err := s.npc.Create(ctx, id, home.Ref, trader.Kind); err != nil {
		return fmt.Errorf("npc_ships: %w", err)
	}
	return nil
}

// spawnMiner persists one miner: a ship row owned by the system player, its
// miner ai_state (home factory + ore type + initial asteroid target), and an
// npc_ships identity row (kind "miner").
func (s *npcSpawner) spawnMiner(
	ctx context.Context,
	owner domain.PlayerID,
	f domain.Station,
	home miner.Leg,
	ore domain.GoodsTypeID,
	target miner.Target,
	idx int,
) error {
	id, err := s.ships.Create(ctx, s.newNPCShip(owner, f.SectorID, f.Pos, f.Race, idx))
	if err != nil {
		return fmt.Errorf("create ship: %w", err)
	}

	stateJSON, err := miner.NewInitialState(home, ore, target)
	if err != nil {
		return fmt.Errorf("miner state: %w", err)
	}
	if err := s.aiState.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         id,
		SectorID:       f.SectorID,
		ControllerKind: miner.Kind,
		StateJSON:      stateJSON,
	}}); err != nil {
		return fmt.Errorf("ai state: %w", err)
	}

	if err := s.npc.Create(ctx, id, home.Ref, miner.Kind); err != nil {
		return fmt.Errorf("npc_ships: %w", err)
	}
	return nil
}

// spawnPassengersFor tops up the passenger fleet of one civilian trade
// station: it builds the home-relative destination pool and spawns the
// missing passenger TS. Skips (logged) when the pool has no destination other
// than home — the TS would have nowhere to ferry to.
func (s *npcSpawner) spawnPassengersFor(
	ctx context.Context,
	owner domain.PlayerID,
	ts tradeStationRef,
	dests []passengerDest,
	served map[npcshipsrepo.HomeKind]int,
) (int, error) {
	have := served[npcshipsrepo.HomeKind{Home: ts.ref, Kind: passenger.Kind}]
	if have >= s.cfg.PassengersPerTradeStation {
		return 0, nil
	}
	pool := s.passengerPool(ts.sector, dests)
	if !poolHasOtherThan(pool, ts.ref) {
		s.logger.Warn("npc spawn: no passenger destinations near trade station",
			"trade_station", ts.id, "sector", int64(ts.sector))
		return 0, nil
	}

	home := passenger.Leg{Sector: ts.sector, Pos: ts.pos, Ref: ts.ref}
	spawned := 0
	for i := have; i < s.cfg.PassengersPerTradeStation; i++ {
		if err := s.spawnPassenger(ctx, owner, home, pool, ts.race, i); err != nil {
			return spawned, fmt.Errorf("spawn passenger for trade station %d: %w", ts.id, err)
		}
		spawned++
	}
	s.logger.Info("npc passengers assigned to trade station",
		"trade_station", ts.id, "sector", int64(ts.sector), "pool", len(pool))
	return spawned, nil
}

// spawnPassenger persists one passenger TS: a ship row at the home trade
// station, its passenger ai_state (home + destination pool + a per-ship
// round-robin offset), and an npc_ships identity row (kind "passenger").
func (s *npcSpawner) spawnPassenger(
	ctx context.Context,
	owner domain.PlayerID,
	home passenger.Leg,
	pool []passenger.Leg,
	race int,
	idx int,
) error {
	id, err := s.ships.Create(ctx, s.newNPCShip(owner, home.Sector, home.Pos, race, idx))
	if err != nil {
		return fmt.Errorf("create ship: %w", err)
	}

	stateJSON, err := passenger.NewInitialState(home, pool, idx)
	if err != nil {
		return fmt.Errorf("passenger state: %w", err)
	}
	if err := s.aiState.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         id,
		SectorID:       home.Sector,
		ControllerKind: passenger.Kind,
		StateJSON:      stateJSON,
	}}); err != nil {
		return fmt.Errorf("ai state: %w", err)
	}

	if err := s.npc.Create(ctx, id, home.Ref, passenger.Kind); err != nil {
		return fmt.Errorf("npc_ships: %w", err)
	}
	return nil
}

// nearestTradeStation returns the reachable trade station with the fewest
// gate hops from sector `from` (0 hops = same sector), tie-broken by id for
// determinism. ok=false when none is reachable.
func (s *npcSpawner) nearestTradeStation(from domain.SectorID, candidates []tradeStationRef) (tradeStationRef, bool) {
	var best tradeStationRef
	bestHops := -1
	found := false
	for _, ts := range candidates {
		hops, ok := s.router.Hops(from, ts.sector)
		if !ok {
			continue
		}
		if !found || hops < bestHops || (hops == bestHops && ts.id < best.id) {
			best, bestHops, found = ts, hops, true
		}
	}
	return best, found
}

// asteroidRef is a miner-target candidate flattened from the per-sector
// asteroids, used by nearestAsteroid.
type asteroidRef struct {
	id      domain.AsteroidID
	sector  domain.SectorID
	pos     domain.Vec2
	oreType domain.GoodsTypeID
}

// nearestAsteroid returns the reachable asteroid of the given ore type with
// the fewest gate hops from sector `from` (0 hops = same sector), tie-broken
// by id for determinism. ok=false when none of that ore type is reachable.
func (s *npcSpawner) nearestAsteroid(from domain.SectorID, ore domain.GoodsTypeID, candidates []asteroidRef) (miner.Target, bool) {
	var best asteroidRef
	bestHops := -1
	found := false
	for _, a := range candidates {
		if a.oreType != ore {
			continue
		}
		hops, ok := s.router.Hops(from, a.sector)
		if !ok {
			continue
		}
		if !found || hops < bestHops || (hops == bestHops && a.id < best.id) {
			best, bestHops, found = a, hops, true
		}
	}
	if !found {
		return miner.Target{}, false
	}
	return miner.Target{ID: best.id, Sector: best.sector, Pos: best.pos}, true
}

// collectAsteroids flattens every asteroid across sectors.
func collectAsteroids(asteroids map[domain.SectorID][]domain.Asteroid) []asteroidRef {
	var out []asteroidRef
	for _, list := range asteroids {
		for _, a := range list {
			out = append(out, asteroidRef{
				id:      a.ID,
				sector:  a.SectorID,
				pos:     a.Pos,
				oreType: a.OreType,
			})
		}
	}
	return out
}

// collectTradeStations flattens every trade station across sectors.
func collectTradeStations(statics map[domain.SectorID]domain.SectorStatics) []tradeStationRef {
	var out []tradeStationRef
	for _, st := range statics {
		for _, ts := range st.TradeStations {
			out = append(out, tradeStationRef{
				sector: ts.SectorID,
				pos:    ts.Pos,
				ref:    ts.ObjectID(),
				id:     int64(ts.ID),
				race:   ts.Race,
			})
		}
	}
	return out
}

// passengerDest is a passenger cruise-target candidate (any station or trade
// station) flattened from the per-sector statics.
type passengerDest struct {
	sector domain.SectorID
	pos    domain.Vec2
	ref    domain.EntityRef
	race   int
}

// collectPassengerDests flattens every station and trade station across
// sectors as passenger cruise candidates (race filtered at pool-build time).
func collectPassengerDests(statics map[domain.SectorID]domain.SectorStatics) []passengerDest {
	var out []passengerDest
	for _, st := range statics {
		for _, s := range st.Stations {
			out = append(out, passengerDest{sector: s.SectorID, pos: s.Pos, ref: s.ObjectID(), race: s.Race})
		}
		for _, ts := range st.TradeStations {
			out = append(out, passengerDest{sector: ts.SectorID, pos: ts.Pos, ref: ts.ObjectID(), race: ts.Race})
		}
	}
	return out
}

// passengerPool builds the home-relative cruise pool: every civilian (race
// 1-4) station/trade station within PassengerRouteRadius gate hops of the home
// sector (home itself included — pickNext skips the current stop). Sorted by
// (kind, id) so the pool is deterministic across cold-starts.
func (s *npcSpawner) passengerPool(homeSector domain.SectorID, dests []passengerDest) []passenger.Leg {
	var pool []passenger.Leg
	for _, d := range dests {
		if d.race < 1 || d.race > passengerRaceMax {
			continue
		}
		hops, ok := s.router.Hops(homeSector, d.sector)
		if !ok || hops > s.cfg.PassengerRouteRadius {
			continue
		}
		pool = append(pool, passenger.Leg{Sector: d.sector, Pos: d.pos, Ref: d.ref})
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].Ref.Kind != pool[j].Ref.Kind {
			return pool[i].Ref.Kind < pool[j].Ref.Kind
		}
		return pool[i].Ref.ID < pool[j].Ref.ID
	})
	return pool
}

// poolHasOtherThan reports whether the pool contains a destination different
// from ref — i.e. the passenger has somewhere to go besides its home.
func poolHasOtherThan(pool []passenger.Leg, ref domain.EntityRef) bool {
	for _, leg := range pool {
		if leg.Ref != ref {
			return true
		}
	}
	return false
}

// collectFactories returns every station across sectors in a deterministic
// (sector, id) order so the spawn assigns the same routes on every cold-start.
func collectFactories(statics map[domain.SectorID]domain.SectorStatics) []domain.Station {
	var out []domain.Station
	for _, st := range statics {
		out = append(out, st.Stations...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SectorID != out[j].SectorID {
			return out[i].SectorID < out[j].SectorID
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// traderHauler is the sector.TraderLogistics implementation over cargo.Service
// (phase 5.3). It hauls min(available-at-source, maxUnits) of a good and lets
// the destination's free space cap the rest: an overfull destination yields
// cargo.ErrNoSpace, which is treated as "haul nothing this tick" rather than a
// failure. Logistics are free — no cash changes hands.
type traderHauler struct {
	cargo *cargo.Service
}

func (h traderHauler) Haul(ctx context.Context, from, to domain.EntityRef, gtype domain.GoodsTypeID, maxUnits int64) error {
	// viewer 0: NPC trader reads the source's unowned pool (its own ship hold
	// or a station's unowned goods), never a player's personal stack (10.22).
	inv, err := h.cargo.Inventory(ctx, from, 0)
	if err != nil {
		return fmt.Errorf("read source inventory: %w", err)
	}
	have := int64(0)
	for _, item := range inv.Items {
		if item.GoodsType == gtype {
			have = item.Quantity
			break
		}
	}
	qty := have
	if qty > maxUnits {
		qty = maxUnits
	}
	if qty <= 0 {
		return nil
	}
	// actor 0: NPC hauls deposit into / draw from a station's unowned pool, not
	// any player's personal stack (phase 10.22).
	if err := h.cargo.Move(ctx, 0, from, to, gtype, qty); err != nil {
		if errors.Is(err, cargo.ErrNoSpace) {
			return nil // destination is full this tick — try again next loop
		}
		return err
	}
	return nil
}

// minerHauler is the sector.MinerLogistics implementation over cargo.Service
// (phase 5.4): it deposits freshly drilled ore into the miner ship's hold,
// capacity-checked. ErrNoSpace is surfaced (not swallowed) so the worker
// leaves the asteroid intact for the next tick rather than losing the ore.
type minerHauler struct {
	cargo *cargo.Service
}

func (h minerHauler) AddOre(ctx context.Context, ship domain.EntityRef, ore domain.GoodsTypeID, qty int64) error {
	return h.cargo.Add(ctx, ship, ore, qty)
}

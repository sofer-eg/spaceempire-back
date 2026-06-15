package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/miner"
	"spaceempire/back/internal/ai/passenger"
	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/ai/trader"
	"spaceempire/back/internal/api"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/auction"
	"spaceempire/back/internal/economy/insurance"
	"spaceempire/back/internal/economy/production"
	"spaceempire/back/internal/economy/rent"
	"spaceempire/back/internal/observ"
	aistaterepo "spaceempire/back/internal/persistence/aistate"
	asteroidsrepo "spaceempire/back/internal/persistence/asteroids"
	auctionrepo "spaceempire/back/internal/persistence/auction"
	bountiesrepo "spaceempire/back/internal/persistence/bounties"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	containersrepo "spaceempire/back/internal/persistence/containers"
	dronesrepo "spaceempire/back/internal/persistence/drones"
	insurancerepo "spaceempire/back/internal/persistence/insurance"
	lasertowersrepo "spaceempire/back/internal/persistence/lasertowers"
	npcshipsrepo "spaceempire/back/internal/persistence/npcships"
	playersrepo "spaceempire/back/internal/persistence/players"
	questsrepo "spaceempire/back/internal/persistence/quests"
	rentsrepo "spaceempire/back/internal/persistence/rents"
	satellitesrepo "spaceempire/back/internal/persistence/satellites"
	shipsrepo "spaceempire/back/internal/persistence/ships"
	stationsrepo "spaceempire/back/internal/persistence/stations"
	traderepo "spaceempire/back/internal/persistence/trade"
	worldrepo "spaceempire/back/internal/persistence/world"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/pkg/config"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/quest"
	raceref "spaceempire/back/internal/reference/race"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/social/bounties"
	"spaceempire/back/internal/social/clans"
	"spaceempire/back/internal/social/racestanding"
	"spaceempire/back/internal/social/relations"
	"spaceempire/back/internal/trade"
	"spaceempire/back/internal/webui"
	"spaceempire/back/internal/world"
)

// defaultSectorID is the sector new players spawn in until the lobby/sector
// chooser arrives (phase 2.5+). Auth-side spawner and HTTP-side world endpoint
// both reference this constant.
const defaultSectorID = domain.SectorID(1)

// Passenger-TS tuning (phase 5.5), ports of the old npc_fab_config knobs.
// passenger_route_radius / passenger_ships_per_ts live in NPCSpawnerConfig.
const (
	// passengerDockWaitSeconds is how long a passenger TS lingers at a station
	// before departing; converted to ticks against the tick interval.
	passengerDockWaitSeconds = 50.0
	// passengerMaxOnBoard caps the random passenger batch boarded on departure
	// (≈ the old floor(cargobay/3) for the default 100-unit hold).
	passengerMaxOnBoard = 33
)

// Run wires the sector worker pool and the HTTP/WS server, then blocks
// until ctx is canceled. On cancel it shuts the HTTP server down with
// cfg.Server.ShutdownTimeout and waits for the worker goroutines to exit.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	goodsBal, err := balance.LoadFromFile(cfg.Balance.Path)
	if err != nil {
		return fmt.Errorf("load balance: %w", err)
	}

	// Production recipes live in station_types.yaml (phase 8.15), not in
	// balance.yaml. Load the station-type catalog + recipes, then fold the
	// recipes into the balance catalog — balance.New re-validates every recipe
	// line against the goods catalog.
	stationTypes, recipes, err := balance.LoadStationTypesFromFile(cfg.Balance.StationTypesPath)
	if err != nil {
		return fmt.Errorf("load station types: %w", err)
	}
	bal, err := balance.New(goodsBal.AllGoods(), recipes)
	if err != nil {
		return fmt.Errorf("build balance with recipes: %w", err)
	}
	logger.Info("balance loaded", "path", cfg.Balance.Path, "goods", bal.GoodsCount(), "recipes", bal.RecipeCount())
	logger.Info("station types loaded", "path", cfg.Balance.StationTypesPath, "types", stationTypes.StationTypeCount())

	shipClasses, err := balance.LoadShipClassesFromFile(cfg.Balance.ShipClassesPath)
	if err != nil {
		return fmt.Errorf("load ship classes: %w", err)
	}
	logger.Info("ship classes loaded", "path", cfg.Balance.ShipClassesPath, "classes", shipClasses.ShipClassCount())

	equipment, err := balance.LoadEquipmentFromFile(cfg.Balance.EquipmentPath)
	if err != nil {
		return fmt.Errorf("load equipment: %w", err)
	}
	logger.Info("equipment loaded", "path", cfg.Balance.EquipmentPath, "rows", equipment.EquipmentCount())

	pool, err := database.NewPool(ctx, database.Config{
		DSN:         cfg.Postgres.DSN,
		MaxConns:    cfg.Postgres.MaxConns,
		ConnTimeout: cfg.Postgres.ConnTimeout,
		AutoMigrate: cfg.Postgres.AutoMigrate,
	})
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer pool.Close()

	realClock := clock.NewRealClock()

	// Observability (7.1): Prometheus registry + collectors. Passed to the
	// sector pool (WithMetrics) and exposed via /metrics; the DB pool gauges
	// read live from pool.Stat on each scrape.
	metrics := observ.NewMetrics()
	metrics.RegisterPoolStats(pool)

	shipRepo := shipsrepo.New(pool)
	droneRepo := dronesrepo.New(pool)
	aiStateRepo := aistaterepo.New(pool)
	clansRepo := clans.NewRepository(pool)
	worldRepo := worldrepo.New(pool)
	stationsRepo := stationsrepo.New(pool)
	laserTowersRepo := lasertowersrepo.New(pool)
	satellitesRepo := satellitesrepo.New(pool)
	asteroidsRepo := asteroidsrepo.New(pool)
	cargoRepoPersistence := cargorepo.New(pool)
	txManager := database.NewTxManager(pool)
	containerRepo := containersrepo.New(pool, txManager)
	cargoSvc := cargo.New(cargoRepoPersistence, cargo.NewRepoTxRunner(txManager, cargoRepoPersistence))
	tradeRepoPersistence := traderepo.New(pool)
	playersRepoPersistence := playersrepo.New(pool)
	tradePool := trade.NewPoolRepo(tradeRepoPersistence, playersRepoPersistence, cargoRepoPersistence)
	tradeSvc := trade.New(tradePool, trade.NewRepoTxRunner(txManager, tradePool), bal)
	auctionRepoPersistence := auctionrepo.New(pool)
	auctionTx := auction.NewRepoTxRunner(txManager, auctionRepoPersistence, playersRepoPersistence, cargoRepoPersistence)
	auctionSvc := auction.New(auctionTx, realClock, logger)
	auctionCloser := auction.NewCloser(auctionSvc, realClock, logger, time.Second)
	sectors, gates, err := worldRepo.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load world: %w", err)
	}
	topology := world.New(sectors, gates)

	sectorIDs := make([]domain.SectorID, len(sectors))
	statics := make(map[domain.SectorID]domain.SectorStatics, len(sectors))
	asteroids := make(map[domain.SectorID][]domain.Asteroid, len(sectors))
	// Pass 1: load each sector's static layout (stations, trade stations,
	// towers) plus its asteroids. The NPC spawner needs both to assign trader
	// routes and miner targets before any ship is loaded into the workers.
	for i, s := range sectors {
		sectorIDs[i] = s.ID
		sectorStatics, err := stationsRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load statics sector %d: %w", s.ID, err)
		}
		towers, err := laserTowersRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load laser towers sector %d: %w", s.ID, err)
		}
		sectorStatics.LaserTowers = towers
		sats, err := satellitesRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load satellites sector %d: %w", s.ID, err)
		}
		sectorStatics.Satellites = sats
		statics[s.ID] = sectorStatics
		warnOutOfBounds(logger, s.ID, sectorStatics, cfg.Sector.BoundsRadius)

		sectorAsteroids, err := asteroidsRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load asteroids sector %d: %w", s.ID, err)
		}
		asteroids[s.ID] = sectorAsteroids
	}

	pathRouter := world.NewPathRouter(topology, nil)

	// NPC traders (5.3): create trader ships + ai_state + npc_ships for every
	// producing factory not yet served, BEFORE loading ships so the cold-start
	// pass below hydrates them. Idempotent across restarts (npc_ships).
	spawnCfg := ShipSpawnerConfig{SectorID: defaultSectorID}.withDefaults()
	npcRepo := npcshipsrepo.New(pool)
	npcSpawner := newNPCSpawner(shipRepo, aiStateRepo, npcRepo, bal, shipClasses, pathRouter, spawnCfg, NPCSpawnerConfig{}, logger)
	if err := npcSpawner.EnsureSpawned(ctx, statics, asteroids); err != nil {
		return fmt.Errorf("npc spawn: %w", err)
	}

	// Race fleets (9.3): Navy (races 1-5) + pirates (6) warships anchored at
	// their stations with controller_kind=race, hydrated by Pass 2 below like
	// the fab-ships. Idempotent top-up to race_fleet_limit — npc_ships cascades
	// on death, so a restart refills the fleet.
	raceFleet := newRaceFleetSpawner(shipRepo, aiStateRepo, npcRepo, shipClasses, spawnCfg, RaceFleetConfig{}, logger)
	if err := raceFleet.EnsureSpawned(ctx, statics); err != nil {
		return fmt.Errorf("race fleet spawn: %w", err)
	}

	// System NPC player id, reused by the race-tower predicate (8.3, exclude
	// NPC ships from race towers) and the bounty NPC-killer guard (8.2).
	// EnsureSpawned above already validated it exists; 0 disables both guards.
	npcPlayerID, err := npcRepo.SystemPlayerID(ctx)
	if err != nil {
		logger.Warn("npc system player lookup", "err", err)
		npcPlayerID = 0
	}

	// Pass 2: load ships / drones / containers / ai_state (now including the
	// NPC rows the spawner just inserted).
	initial := make(map[domain.SectorID][]domain.Ship, len(sectors))
	initialDrones := make(map[domain.SectorID][]domain.Drone, len(sectors))
	initialContainers := make(map[domain.SectorID][]domain.Container, len(sectors))
	initialAIState := make(map[domain.SectorID][]domain.AIState, len(sectors))
	for _, s := range sectors {
		ships, err := shipRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load ships sector %d: %w", s.ID, err)
		}
		initial[s.ID] = ships

		sectorDrones, err := droneRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load drones sector %d: %w", s.ID, err)
		}
		initialDrones[s.ID] = sectorDrones

		sectorContainers, err := containerRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load containers sector %d: %w", s.ID, err)
		}
		initialContainers[s.ID] = sectorContainers

		sectorAIState, err := aiStateRepo.LoadAll(ctx, s.ID)
		if err != nil {
			return fmt.Errorf("load ai state sector %d: %w", s.ID, err)
		}
		initialAIState[s.ID] = sectorAIState

		clampOutOfBounds(logger, s.ID, ships, cfg.Sector.BoundsRadius)
	}

	// Phase 10.23: rebuild each host's RAM PassengerPlayers mirror from the
	// persisted passenger links (players.passenger_of_ship_id). The mirror is
	// RAM-only, so a restart would otherwise lose who is riding which ship —
	// breaking jump fan-out (B6) and death ejection (B8) until the rider acts.
	if links, err := playersRepoPersistence.PassengerLinks(ctx); err != nil {
		return fmt.Errorf("load passenger links: %w", err)
	} else if len(links) > 0 {
		riders := make(map[domain.ShipID][]domain.PlayerID, len(links))
		for _, l := range links {
			riders[l.HostShipID] = append(riders[l.HostShipID], l.PlayerID)
		}
		for sid := range initial {
			for i := range initial[sid] {
				if ps, ok := riders[initial[sid][i].ID]; ok {
					initial[sid][i].PassengerPlayers = ps
				}
			}
		}
	}

	jumpBus := bus.NewInMemory(64)
	defer jumpBus.Close()

	// Relations (6.2) — load the declared-relation cache for hostility
	// lookups. RaceAI (5.2) is its first consumer; 6.2a will add combat
	// gating. Precount at startup so Get is primed before the first tick.
	relationsSvc := relations.New(relations.NewRepository(pool), clansRepo)
	if err := relationsSvc.Precount(ctx); err != nil {
		return fmt.Errorf("precount relations: %w", err)
	}

	// Race standing (9.4) — per-player reputation with each NPC race, primed
	// at startup so the wanted overlay and police scan resolve from RAM. The
	// main-race navy/police drop it on contraband busts and faction-ship kills.
	standingSvc := racestanding.New(racestanding.NewRepository(pool), racestanding.Config{})
	if err := standingSvc.Precount(ctx); err != nil {
		return fmt.Errorf("precount race standing: %w", err)
	}

	// AI registry (5.1). Phase 5.2 registers the "race" NPC controller,
	// resolving hostility through the relations Service. Traders/miners/
	// passengers join in 5.3–5.5.
	aiRegistry := ai.NewRegistry()
	// 9.1: race ships fight by the DefaultStanding matrix (8.13) on ship.Race,
	// not player/clan relations (all NPC factions share the __npc__ owner).
	// 9.4: the wanted overlay adds "a main race attacks a wanted player" on top
	// of the matrix, so navy/police engage players flagged by contraband busts.
	race.Register(aiRegistry, wantedOverlayTargeter{
		base:     raceMatrixTargeter{},
		standing: standingSvc,
		npc:      npcPlayerID,
	}, race.Config{})
	// 5.3: NPC TS traders. ArriveRadius must exceed the autopilot's park
	// distance (DockRange/2) so a parked trader is recognised as arrived.
	trader.Register(aiRegistry, trader.Config{ArriveRadius: cfg.Sector.DockRange * 2})
	// 5.4: NPC mining TS. Same arrival threshold for the home factory.
	miner.Register(aiRegistry, miner.Config{ArriveRadius: cfg.Sector.DockRange * 2})
	// 5.5: NPC passenger TS. The controller has no clock, so the 50s dock wait
	// is converted to whole ticks against the configured tick interval.
	passengerDockWaitTicks := int(math.Ceil(passengerDockWaitSeconds / cfg.Sector.TickInterval.Seconds()))
	if passengerDockWaitTicks < 1 {
		passengerDockWaitTicks = 1
	}
	passenger.Register(aiRegistry, passenger.Config{
		ArriveRadius:  cfg.Sector.DockRange * 2,
		DockWaitTicks: passengerDockWaitTicks,
		MaxPassengers: passengerMaxOnBoard,
	})

	productionSvc, err := production.New(bal, production.NewRepoTxRunner(txManager, tradeRepoPersistence, stationsRepo))
	if err != nil {
		return fmt.Errorf("init production: %w", err)
	}
	productionReader := production.NewReader(bal, stationsRepo)

	sectorPool := sector.NewPool(
		sector.PoolConfig{
			WorkersCount: cfg.Sector.WorkersCount,
			Worker: sector.Config{
				TickInterval:     cfg.Sector.TickInterval,
				SnapshotInterval: cfg.Sector.SnapshotInterval,
				InboxCapacity:    cfg.Sector.InboxCapacity,
				DockRange:        cfg.Sector.DockRange,
				GateRange:        cfg.Sector.GateRange,
				ShutdownTimeout:  cfg.Server.ShutdownTimeout,
				// 10.3.6: per-tick "action" energy a player drill spends, from the
				// up_drill catalog row (uniform across class tiers).
				MineEnergyCost: equipmentEnergyUsage(equipment, "up_drill"),
			},
		},
		sectorIDs,
		realClock,
		shipRepo,
		logger,
		initial,
		sector.WithHandoff(topology, jumpBus),
		sector.WithRouter(pathRouter),
		sector.WithStatics(statics),
		sector.WithProduction(productionSvc),
		sector.WithDrones(droneRepo, initialDrones),
		sector.WithContainers(containerRepo, initialContainers),
		sector.WithAI(aiRegistry, aiStateRepo, initialAIState),
		// 5.3: NPC traders' ai.Transfer hauls cargo between stations and ships
		// via cargo.Service (free logistics, no cash).
		sector.WithTraderLogistics(traderHauler{cargo: cargoSvc}),
		// 5.4: minable asteroids + NPC miners' ai.Mine ore deposit into the hold.
		sector.WithAsteroids(asteroidsRepo, asteroids),
		sector.WithMinerLogistics(minerHauler{cargo: cargoSvc}),
		// 6.2a: hostility into combat. Towers fire at their owner's hostiles
		// (NPC towers — nil owner — stay passive until race-standing, so the
		// spawn-sector seed tower never shoots players). Lasers/drones get the
		// ship-vs-ship oracle for friendly-fire gating and drone auto-acquire.
		sector.WithHostility(func(owner *domain.PlayerID, ship *domain.Ship) bool {
			if owner == nil {
				return false
			}
			return relationsSvc.IsHostile(domain.PlayerRef(*owner), domain.PlayerRef(ship.PlayerID))
		}),
		sector.WithRelations(relationsSvc),
		// 7.1: per-tick telemetry (tick duration, ship count, queue depth,
		// overruns, handoffs, time-scale) into the Prometheus registry.
		sector.WithMetrics(metrics),
		// 8.3/8.13: race-owned laser towers of a hostile race (pirate/xenon/
		// kha'ak) fire at real-player ships. Hostility now comes from the race
		// reference's default standing matrix (race.IsHostile) instead of a
		// hardcoded {6,7,8} set; a factionless player is the neutral race. NPC
		// ships (system player) are excluded so race towers don't trigger
		// NPC-vs-NPC combat.
		sector.WithRaceHostility(func(towerRace int, ship *domain.Ship) bool {
			if ship.PlayerID == npcPlayerID {
				return false
			}
			return raceref.IsHostile(domain.RaceID(towerRace), raceref.Neutral)
		}),
		// 8.5: persist laser-tower destruction so a killed tower stays dead
		// across restarts (other statics remain RAM-only this phase).
		sector.WithTowerPersistence(laserTowersRepo),
		// 10.15: player-deployed navigation satellites — persist install/destroy
		// so a deployed satellite survives a restart and a killed one stays dead.
		sector.WithSatellites(satellitesRepo),
		// 9.4: police — the main-race navy scans player ships for contraband,
		// confiscates it (via cargo), drops the player's race standing, and
		// drops standing again when a player destroys a faction ship.
		sector.WithPolice(
			policeScanner{cargo: cargoSvc, standing: standingSvc, npc: npcPlayerID, cfg: PoliceScanConfig{}.withDefaults()},
			sector.PoliceConfig{Races: policeRaces()},
		),
		// 10.3.13: combat reputation — destroying a ship grows the attributed
		// killer's war_rate (the rank gate in 10.3.4 reads it). NPC kills are
		// skipped inside the awarder.
		sector.WithReputation(reputationAwarder{players: playersRepoPersistence, npc: npcPlayerID}),
	)

	// spawnCfg was materialised above (with defaults applied) so the player
	// spawner and the welcome message agree on the ship's starting HP/Shield —
	// the SPA uses these as the maxima for its hull/shield bars.
	// Phase 10.10: a player spawns their starter ship at their race's home NPC
	// shipyard, with that race and its M5 model name. authRepo doubles as the
	// player-race reader; buildHomeShipyards maps race → home shipyard from the
	// loaded statics; shipClasses names the starter ship.
	authRepo := auth.NewRepository(pool)
	spawner := newShipSpawner(shipRepo, cargoRepoPersistence, sectorPool, spawnCfg, authRepo, buildHomeShipyards(statics), shipClasses)

	authSvc := auth.NewService(authRepo, realClock, spawner, auth.ServiceConfig{
		SessionTTL: cfg.Auth.SessionTTL,
		BcryptCost: cfg.Auth.BcryptCost,
	})
	authSrv := auth.NewServer(authSvc, auth.ServerConfig{
		CookieSecure:      cfg.Auth.CookieSecure,
		SessionTTLSeconds: int(cfg.Auth.SessionTTL.Seconds()),
	}, logger)

	srv := api.NewServer(sectorPool, api.Config{
		SnapshotInterval:   cfg.Sector.TickInterval,
		SectorBoundsRadius: cfg.Sector.BoundsRadius,
		NearZoomRadius:     cfg.Sector.NearZoomRadius,
		DockRange:          cfg.Sector.DockRange,
		GateRange:          cfg.Sector.GateRange,
		MaxHP:              spawnCfg.StartHP,
		MaxShield:          spawnCfg.StartShld,
		// Commands are drained once per tick, so the handler must wait at
		// least one full tick for the worker to apply and reply. Phase 3.9
		// pushed TickInterval to 3s — keep AckTimeout = TickInterval + 1s
		// so dock/jump/move never time out under normal load.
		AckTimeout:        cfg.Sector.TickInterval + time.Second,
		SectorID:          int64(defaultSectorID),
		AuthMiddleware:    authSrv.RequireAuth,
		Topology:          topology,
		PathRouter:        pathRouter,
		Cargo:             cargoSvc,
		Trade:             tradeSvc,
		StationProduction: productionReader,
		Auction:           auctionSvc,
		Goods:             bal,
		ShipClasses:       shipClasses,
		StationTypes:      stationTypes,
		Equipment:         equipment,
		HandoffBus:        jumpBus,
		EventBus:          jumpBus,
		MissileCargo:      cargoSvc,
		SatelliteCargo:    cargoSvc,
		DroneCargo:        cargoSvc,
		NpcPlayerID:       npcPlayerID,
		ActiveShips:       playersRepoPersistence,
		ActiveShipWriter:  playersRepoPersistence,
		HandoffPublisher:  jumpBus,
		Fleet:             sectorPool,
	}, logger)
	authSrv.RegisterRoutes(srv.Mux())
	authSrv.RegisterPlayersList(srv.Mux())
	authSrv.RegisterPlayerSelf(srv.Mux(), playersRepoPersistence)

	clansSrv := clans.NewServer(clans.NewService(clansRepo), logger)
	clansSrv.RegisterRoutes(srv.Mux(), authSrv.RequireAuth)

	// Shipyard ship recovery (10.2): a spacesuit pilot docked at a shipyard
	// exchanges the suit for a fresh starter ship at the same spot.
	newShipyardServer(sectorPool, spawner, logger).RegisterRoutes(srv.Mux(), authSrv.RequireAuth)

	// Shipyard purchase + outfitting (10.14): buy a class ship for credits and
	// install/remove ct_updates modules on a docked ship. Shares the spawn
	// config (base stats), the ship-class/equipment catalogs and authRepo (race
	// reader) with the spawner; runs cash debit + persist in a tx via txManager.
	newOutfitServer(sectorPool, shipRepo, playersRepoPersistence, txManager, shipClasses, equipment, authRepo, standingSvc, spawnCfg, logger).
		RegisterRoutes(srv.Mux(), authSrv.RequireAuth)

	// EVA (10.23): exit ship into a spacesuit, ship access toggle, board / ride
	// as passenger, disembark. Shares the pool, the spawner (spacesuit), the
	// players repo (active/passenger pointers) and the handoff bus.
	newEvaServer(sectorPool, spawner, playersRepoPersistence, jumpBus, npcPlayerID, EVAConfig{}, logger).
		RegisterRoutes(srv.Mux(), authSrv.RequireAuth)

	// Race standing (9.4): GET /api/my/race-standings for the reputation panel,
	// plus a background closer that slowly decays every standing toward neutral.
	racestanding.NewServer(standingSvc, logger).RegisterRoutes(srv.Mux(), authSrv.RequireAuth)
	standingCloser := racestanding.NewCloser(standingSvc, realClock, logger, time.Hour)

	// Bounties (6.3): player/clan-funded contracts. The Service shares
	// txManager with trade/auction; payouts are driven off the kill bus and
	// expiry by a background closer (started below).
	bountyPool := bounties.NewPoolRepo(bountiesrepo.New(pool), playersRepoPersistence, clansRepo)
	// npcPlayerID (resolved above) makes NPC kills not claim bounties — the
	// reward stays open for a real hunter.
	bountySvc := bounties.New(
		bountyPool,
		bounties.NewRepoTxRunner(txManager, bountyPool),
		bounties.NewClansAdapter(clansRepo),
		realClock, logger, bounties.Config{IgnoreKiller: npcPlayerID},
	)
	bounties.NewServer(bountySvc, logger).RegisterRoutes(srv.Mux(), authSrv.RequireAuth)
	// Subscribe to the same bus the sector workers publish kills to. The
	// handler does its DB payout in the bus subscriber goroutine; the 64-deep
	// per-subscriber buffer absorbs kill bursts without stalling a tick.
	if err := jumpBus.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		var ev sector.EntityKilledEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			logger.Error("bounties: decode entity_killed", "err", err)
			return
		}
		if ev.Victim.Kind != domain.EntityKindShip {
			return
		}
		if err := bountySvc.OnKill(ctx, ev.Killer, ev.VictimPlayer); err != nil {
			logger.Error("bounties: payout", "err", err,
				"killer", int64(ev.Killer), "victim", int64(ev.VictimPlayer))
		}
	}); err != nil {
		return fmt.Errorf("subscribe bounty payouts: %w", err)
	}
	bountyCloser := bounties.NewCloser(bountySvc, realClock, logger, time.Second)

	// Rent (6.4): ownership upkeep on player-owned stations/shipyards/trade-
	// stations. The pool composes rents+players+stations for the billing tx;
	// overdue notifications are published to the same bus the WS reads.
	rentPool := rent.NewPoolRepo(rentsrepo.New(pool), playersRepoPersistence, stationsRepo)
	rentSvc := rent.New(
		rentPool, rentPool,
		rent.NewRepoTxRunner(txManager, rentPool),
		jumpBus, realClock, logger, rent.Config{},
	)
	rent.NewServer(rentSvc, logger).RegisterRoutes(srv.Mux(), authSrv.RequireAuth)
	// Prime rent rows for already player-owned objects so billing starts on
	// the next tick without waiting for the first reconcile.
	if err := rentSvc.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile rents: %w", err)
	}
	rentCloser := rent.NewCloser(rentSvc, realClock, logger, time.Hour)

	// Insurance (6.5): ship destruction cover. Buy debits the premium; payout
	// listens on the same kill bus as bounties (a second subscriber) and pays
	// the holder when an insured ship dies.
	insurancePool := insurance.NewPoolRepo(insurancerepo.New(pool), playersRepoPersistence)
	insuranceSvc := insurance.New(
		insurancePool,
		insurance.NewRepoTxRunner(txManager, insurancePool),
		realClock, logger, insurance.Config{},
	)
	insurance.NewServer(insuranceSvc, logger).RegisterRoutes(srv.Mux(), authSrv.RequireAuth)
	if err := jumpBus.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		var ev sector.EntityKilledEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			logger.Error("insurance: decode entity_killed", "err", err)
			return
		}
		if ev.Victim.Kind != domain.EntityKindShip {
			return
		}
		if err := insuranceSvc.OnKill(ctx, domain.ShipID(ev.Victim.ID)); err != nil {
			logger.Error("insurance: payout", "err", err, "ship", ev.Victim.ID)
		}
	}); err != nil {
		return fmt.Errorf("subscribe insurance payouts: %w", err)
	}

	// Spacesuit / EVA respawn (10.1): a third kill-bus subscriber. On a real
	// player's ship death drop the pilot into a weak spacesuit at the death
	// spot; when the spacesuit itself dies respawn a fresh ship at the home
	// shipyard and move the player's WS there (PlayerHandoffEvent).
	respawner := spacesuitRespawner{
		spawner: spawner, bus: jumpBus, players: playersRepoPersistence,
		npc: npcPlayerID, home: spawnCfg.SectorID, logger: logger,
	}
	if err := jumpBus.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		var ev sector.EntityKilledEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			logger.Error("spacesuit: decode entity_killed", "err", err)
			return
		}
		respawner.OnKill(ctx, ev)
	}); err != nil {
		return fmt.Errorf("subscribe spacesuit respawn: %w", err)
	}

	// Quests (8.12): tutorial/mission engine. Lazy-starts the tutorial on the
	// first GET /api/quests/active; a background poller advances steps from
	// player state and grants rewards (reward+advance atomic in one tx).
	questPool := quest.NewPoolRepo(questsrepo.New(pool), playersRepoPersistence)
	// 8.18: quest-NPC spawn lifecycle over the 9.5 runtime spawn machinery.
	questNPCs := &questSpawner{
		ships: shipRepo, aiState: aiStateRepo, pool: sectorPool, topology: topology,
		classes: shipClasses, npc: npcPlayerID, shipCfg: spawnCfg, logger: logger,
	}
	questSvc := quest.New(questPool, quest.NewRepoTxRunner(txManager, questPool), questNPCs, realClock, logger)
	quest.NewServer(questSvc, logger).RegisterRoutes(srv.Mux(), authSrv.RequireAuth)
	questCloser := quest.NewCloser(questSvc, realClock, logger, 5*time.Second)

	// Quest v2 (8.17/8.18): discrete event-driven steps. Translate the kill bus
	// and the api-published cargo/trade signals into quest.Event and feed
	// OnEvent. Every ship death also drives OnShipDestroyed (victim-scoped:
	// target-bound kills + escort failure, regardless of who landed the kill).
	if err := jumpBus.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		var ev sector.EntityKilledEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			logger.Error("quest: decode entity_killed", "err", err)
			return
		}
		if ev.Victim.Kind != domain.EntityKindShip {
			return
		}
		if err := questSvc.OnShipDestroyed(ctx, ev.Victim); err != nil {
			logger.Error("quest: on destroyed", "err", err, "victim", ev.Victim.ID)
		}
		if ev.Killer == 0 {
			return
		}
		if err := questSvc.OnEvent(ctx, quest.Event{
			Player: ev.Killer, Kind: quest.EventKill, Victim: ev.Victim, Amount: 1,
		}); err != nil {
			logger.Error("quest: on kill", "err", err)
		}
	}); err != nil {
		return fmt.Errorf("subscribe quest kills: %w", err)
	}
	if err := jumpBus.Subscribe(ctx, quest.CargoDeliveredTopic, func(payload []byte) {
		var ev quest.CargoDeliveredEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			logger.Error("quest: decode cargo.delivered", "err", err)
			return
		}
		if err := questSvc.OnEvent(ctx, quest.Event{
			Player: ev.Player, Kind: quest.EventDeliver, Target: ev.Target, Goods: ev.Goods, Amount: ev.Qty,
		}); err != nil {
			logger.Error("quest: on deliver", "err", err)
		}
	}); err != nil {
		return fmt.Errorf("subscribe quest deliveries: %w", err)
	}
	if err := jumpBus.Subscribe(ctx, quest.TradeCompletedTopic, func(payload []byte) {
		var ev quest.TradeCompletedEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			logger.Error("quest: decode trade.completed", "err", err)
			return
		}
		if err := questSvc.OnEvent(ctx, quest.Event{
			Player: ev.Player, Kind: quest.EventTrade, Side: ev.Side, Goods: ev.Goods, Amount: ev.Qty,
		}); err != nil {
			logger.Error("quest: on trade", "err", err)
		}
	}); err != nil {
		return fmt.Errorf("subscribe quest trades: %w", err)
	}

	// Dynamic invasion (9.5): a background spawner injects Xenon waves at the
	// gate exits of populated sectors and Kha'ak clusters into sector centres,
	// capped per faction (a wiped wave deletes its rows → frees the cap). It
	// reuses the runtime AddShipCommand spawn path; ships persist via the
	// ship row + race ai_state and reload like any NPC on restart.
	invasion := newInvasionSpawner(
		shipRepo, shipRepo, aiStateRepo, sectorPool,
		topology, statics, shipClasses, npcPlayerID,
		spawnCfg, InvasionConfig{}, realClock, logger,
	)

	// Observability (7.1): /metrics + /debug/* behind the basic-auth gate.
	registerObservability(srv.Mux(), metrics, sectorPool, cfg)

	// Embedded SPA (7.3): method-less catch-all for non-API routes. Every
	// other route is more specific and wins under Go 1.22 ServeMux, so this
	// only serves the frontend + its client-side routes. (Method-less on
	// purpose: a "GET /" would conflict with the method-less /debug/pprof/
	// subtree — neither dominating — and panic at registration.) In dev the
	// SPA is served by Vite (:5173); the committed placeholder keeps the
	// binary self-contained until `make release` embeds the real build.
	srv.Mux().Handle("/", webui.Handler())

	httpSrv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port),
		// Wrap the whole mux: AccessLog assigns a request_id (8.11) + emits a
		// structured access line; HTTPMiddleware times + status-counts every
		// request (including /debug, /metrics).
		Handler:           observ.AccessLog(logger)(metrics.HTTPMiddleware(srv.Handler())),
		ReadHeaderTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := sectorPool.Run(ctx); err != nil {
			logger.Error("sector pool", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		auctionCloser.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		bountyCloser.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		rentCloser.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		questCloser.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		standingCloser.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		invasion.Run(ctx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server starting", "addr", httpSrv.Addr)
		err := httpSrv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-serveErr:
		if err != nil {
			runErr = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "err", err)
	}

	wg.Wait()
	return runErr
}

// warnOutOfBounds logs WARN for any static whose coordinates fall outside the
// sector circle of radius boundsRadius. Statics are seed data and cannot be
// relocated to a fallback, so out-of-bounds ones are surfaced (not corrected)
// to catch seed bugs early. Ships are handled by clampOutOfBounds.
func warnOutOfBounds(
	logger *slog.Logger,
	sectorID domain.SectorID,
	statics domain.SectorStatics,
	boundsRadius float64,
) {
	if boundsRadius <= 0 {
		return
	}
	check := func(kind string, id int64, x, y float64) {
		if math.Hypot(x, y) > boundsRadius {
			logger.Warn("entity outside sector bounds",
				"sectorID", sectorID, "kind", kind, "id", id,
				"x", x, "y", y, "bounds", boundsRadius,
			)
		}
	}
	for _, st := range statics.Stations {
		check("station", int64(st.ID), st.Pos.X, st.Pos.Y)
	}
	for _, sy := range statics.Shipyards {
		check("shipyard", int64(sy.ID), sy.Pos.X, sy.Pos.Y)
	}
	for _, ts := range statics.TradeStations {
		check("trade_station", int64(ts.ID), ts.Pos.X, ts.Pos.Y)
	}
	for _, pb := range statics.Pirbases {
		check("pirbase", int64(pb.ID), pb.Pos.X, pb.Pos.Y)
	}
	for _, lt := range statics.LaserTowers {
		check("laser_tower", int64(lt.ID), lt.Pos.X, lt.Pos.Y)
	}
	for _, sat := range statics.Satellites {
		check("satellite", int64(sat.ID), sat.Pos.X, sat.Pos.Y)
	}
}

// clampOutOfBounds corrects ships loaded with a position outside the sector
// circle of radius boundsRadius. This happens after a crash mid-flight (the
// stored position predates the last immediate Save) or after a jump-handoff
// that bumped sector_id but crashed before the new position was persisted —
// the ship then loads into the new sector still carrying the old sector's
// coordinates. For each offending non-docked ship it falls back to
// FinalTarget.Pos when the final target lives in this sector, else to the
// origin, and logs the correction. Docked ships keep their stored position:
// it is resolved from the static they are parked at on undock.
func clampOutOfBounds(
	logger *slog.Logger,
	sectorID domain.SectorID,
	ships []domain.Ship,
	boundsRadius float64,
) {
	if boundsRadius <= 0 {
		return
	}
	for i := range ships {
		sh := &ships[i]
		if sh.Docked != nil || sh.Pos.Length() <= boundsRadius {
			continue
		}
		old := sh.Pos
		var corrected domain.Vec2
		if sh.FinalTarget != nil && sh.FinalTarget.Sector == sh.SectorID {
			corrected = sh.FinalTarget.Pos
		}
		sh.Pos = corrected
		logger.Warn("ship out of bounds corrected on cold-start",
			"sectorID", sectorID, "shipID", int64(sh.ID),
			"oldX", old.X, "oldY", old.Y,
			"newX", corrected.X, "newY", corrected.Y,
			"bounds", boundsRadius,
		)
	}
}

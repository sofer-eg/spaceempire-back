package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/world"
)

type Config struct {
	// SnapshotInterval is how often WS clients receive snapshot updates.
	// Set to the sector tick interval.
	SnapshotInterval time.Duration
	// SectorBoundsRadius is the half-extent (in world units) the SPA uses
	// to size the sector map. Mirrors cfg.Sector.BoundsRadius. Zero falls
	// back to 5000 in handleWS so legacy tests don't have to set it.
	SectorBoundsRadius float64
	// NearZoomRadius is the half-side of the Near zoom window around the
	// player's own ship. Mirrors cfg.Sector.NearZoomRadius. Zero falls
	// back to 500.
	NearZoomRadius float64
	// DockRange is the world-unit radius the SPA uses to enable the
	// dock affordance for static targets. Mirrors
	// cfg.Sector.DockRange. Zero falls back to 3 in handleWS.
	DockRange float64
	// GateRange is the world-unit radius the SPA uses to enable the
	// jump affordance for gate targets. Mirrors cfg.Sector.GateRange.
	// Zero falls back to 50 in handleWS.
	GateRange float64
	// MaxHP / MaxShield are the ship hull/shield maxima surfaced in the
	// welcome so the SPA can fill the КОРПУС/ЩИТЫ bars. Mirror the
	// spawner's StartHP/StartShld. Zero falls back to 100 in handleWS.
	MaxHP     int
	MaxShield int
	// AckTimeout caps how long a command handler waits for the worker
	// to apply the command and reply. Default 1s.
	AckTimeout time.Duration
	// SectorID is the sector handled by the bound worker. Currently 1 in
	// production; tests pass whatever they configure on the worker.
	SectorID int64
	// AuthMiddleware, if set, wraps endpoints under /api/cmd/* and /ws so
	// they require a valid session cookie. nil leaves them open (used by
	// tests that don't exercise auth).
	AuthMiddleware func(http.Handler) http.Handler
	// Topology backs GET /api/world. nil disables the endpoint (returns
	// 503), which keeps legacy tests that don't load the world working.
	Topology *world.Topology
	// PathRouter backs POST /api/cmd/set-course (autopilot reachability
	// check). nil disables the endpoint with 503; tests that do not exercise
	// the autopilot can leave it unset.
	PathRouter PathRouter
	// Cargo backs the /api/{ship,station,trade-station}/{id}/cargo reads
	// and the POST /api/cmd/cargo/move write. nil disables all four
	// endpoints with 503 (legacy tests that don't load cargo).
	Cargo CargoService
	// Trade backs the /api/{station,trade-station,pirbase}/{id}/market
	// reads and the POST /api/cmd/trade/{buy,sell} writes. nil disables
	// every trade endpoint with 503.
	Trade TradeService
	// StationProduction adorns a Station market with its production-cycle
	// state (the SPA's cycle timer). nil omits the production block.
	StationProduction StationProductionReader
	// Auction backs the /api/auction endpoints (list/create/bid). nil
	// disables them with 503 — useful for tests that don't load auction.
	Auction AuctionService
	// Goods backs GET /api/goods (static catalog used by the SPA to label
	// market/cargo/auction rows). nil disables the endpoint with 503.
	Goods GoodsCatalog
	// ShipClasses backs GET /api/ship-classes (static ship-class catalog
	// from ct_ship_classes). nil disables the endpoint with 503.
	ShipClasses ShipClassCatalog
	// StationTypes backs GET /api/station-types (static station-type catalog
	// from station_types). nil disables the endpoint with 503.
	StationTypes StationTypeCatalog
	// Equipment backs GET /api/equipment (static ship-equipment catalog from
	// ct_updates). nil disables the endpoint with 503.
	Equipment EquipmentCatalog
	// HandoffBus, when set, is the bus the WS handler subscribes to for
	// per-player handoff events. nil disables WS re-subscription on gate
	// jumps — the client must reconnect to see the new sector. Production
	// wires the same in-memory bus that powers sector handoff; tests can
	// leave it nil when they don't exercise the cross-sector path.
	HandoffBus bus.Subscriber
	// EventBus, when set, is where player-action quest events (cargo delivered,
	// trade completed) are published for the quest engine (phase 8.17). nil
	// disables publishing — quests just won't see those discrete signals.
	EventBus bus.Publisher
	// MissileCargo backs POST /api/cmd/launch-missile (cargo Consume /
	// Refund around the sector command). nil disables the endpoint with
	// 503 — legacy tests that don't load cargo can leave it unset.
	MissileCargo MissileCargo
	// DroneCargo backs POST /api/cmd/launch-drone and
	// /api/cmd/recall-drones. nil disables both with 503.
	DroneCargo DroneCargo
	// SatelliteCargo backs POST /api/cmd/install-satellite (cargo Consume /
	// Refund around the sector command, phase 10.15). nil disables the
	// endpoint with 503.
	SatelliteCargo SatelliteCargo
	// NpcPlayerID is the reserved system-player's DB id. Ships owned by this
	// player are NPC (traders, miners, passengers) and the SPA uses IsNPC to
	// colour them amber instead of red. Zero means "no NPC player loaded" and
	// IsNPC stays false for all ships (safe fallback for tests).
	NpcPlayerID domain.PlayerID
	// ActiveShips resolves a player's explicit active ship for WS subscribe
	// (phase 10.14a). nil falls back to the lowest-id rule for every player
	// (tests that don't exercise active-ship selection leave it unset).
	ActiveShips ActiveShipReader
	// ActiveShipWriter persists the active-ship selection for the
	// POST /api/ship/{id}/activate endpoint (10.14a). nil disables it with 503.
	ActiveShipWriter ActiveShipWriter
	// HandoffPublisher publishes the PlayerHandoffEvent that moves a player's
	// WS to the active ship's sector on activate (10.14a). Wired to the same
	// in-memory bus the sector workers use. nil skips the WS move (the client
	// must reconnect to follow a cross-sector switch).
	HandoffPublisher bus.Publisher
	// Fleet lists a player's ships for GET /api/player/ships (10.14a fleet
	// panel). nil disables the endpoint with 503.
	Fleet FleetReader
}

type Server struct {
	router            SectorRouter
	cfg               Config
	npcPlayerID       domain.PlayerID
	logger            *slog.Logger
	mux               *http.ServeMux
	topology          *world.Topology
	pathRouter        PathRouter
	cargo             CargoService
	trade             TradeService
	stationProduction StationProductionReader
	auction           AuctionService
	goods             GoodsCatalog
	shipClasses       ShipClassCatalog
	// hullCategories indexes ship-class id → hull-shape category ("M1".."TS"),
	// built once from shipClasses at construction (phase 10.13). Read by
	// hullCategoryOf to stamp each ship DTO's HullCategory. Reading a missing /
	// nil-map key yields "" — the client then falls back to its heuristic.
	hullCategories map[domain.ShipClassID]string
	stationTypes   StationTypeCatalog
	equipment      EquipmentCatalog
	// launchEnergyCost is the "action" energy a missile launch spends (phase
	// 10.3.1), resolved once from the up_launcher catalog row. 0 when no catalog
	// is wired (tests) — the worker's gate is then a no-op.
	launchEnergyCost int
	handoffBus       bus.Subscriber
	eventBus         bus.Publisher
	missileCargo     MissileCargo
	droneCargo       DroneCargo
	satelliteCargo   SatelliteCargo
	activeShips      ActiveShipReader
	activeShipWriter ActiveShipWriter
	handoffPublisher bus.Publisher
	fleet            FleetReader
}

func NewServer(router SectorRouter, cfg Config, logger *slog.Logger) *Server {
	if cfg.SnapshotInterval <= 0 {
		cfg.SnapshotInterval = time.Second
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		router:            router,
		cfg:               cfg,
		npcPlayerID:       cfg.NpcPlayerID,
		logger:            logger,
		mux:               http.NewServeMux(),
		topology:          cfg.Topology,
		pathRouter:        cfg.PathRouter,
		cargo:             cfg.Cargo,
		trade:             cfg.Trade,
		stationProduction: cfg.StationProduction,
		auction:           cfg.Auction,
		goods:             cfg.Goods,
		shipClasses:       cfg.ShipClasses,
		stationTypes:      cfg.StationTypes,
		equipment:         cfg.Equipment,
		handoffBus:        cfg.HandoffBus,
		eventBus:          cfg.EventBus,
		missileCargo:      cfg.MissileCargo,
		droneCargo:        cfg.DroneCargo,
		satelliteCargo:    cfg.SatelliteCargo,
		activeShips:       cfg.ActiveShips,
		activeShipWriter:  cfg.ActiveShipWriter,
		handoffPublisher:  cfg.HandoffPublisher,
		fleet:             cfg.Fleet,
	}
	s.hullCategories = buildHullCategoryIndex(cfg.ShipClasses)
	s.launchEnergyCost = launchActionEnergyCost(cfg.Equipment)

	s.mux.HandleFunc("GET /healthz", handleHealthz)
	s.mux.Handle("POST /api/cmd/move", s.protect(http.HandlerFunc(s.handleMove)))
	s.mux.Handle("POST /api/cmd/set-course", s.protect(http.HandlerFunc(s.handleSetCourse)))
	s.mux.Handle("POST /api/cmd/dock", s.protect(http.HandlerFunc(s.handleDock)))
	s.mux.Handle("POST /api/cmd/exdock", s.protect(http.HandlerFunc(s.handleExternalDock)))
	s.mux.Handle("POST /api/cmd/undock", s.protect(http.HandlerFunc(s.handleUndock)))
	s.mux.Handle("POST /api/cmd/jump", s.protect(http.HandlerFunc(s.handleJump)))
	s.mux.Handle("POST /api/cmd/attack", s.protect(http.HandlerFunc(s.handleAttack)))
	s.mux.Handle("POST /api/cmd/cease-fire", s.protect(http.HandlerFunc(s.handleCeaseFire)))
	s.mux.Handle("POST /api/cmd/launch-missile", s.protect(http.HandlerFunc(s.handleLaunchMissile)))
	s.mux.Handle("POST /api/cmd/launch-drone", s.protect(http.HandlerFunc(s.handleLaunchDrone)))
	s.mux.Handle("POST /api/cmd/recall-drones", s.protect(http.HandlerFunc(s.handleRecallDrones)))
	s.mux.Handle("POST /api/cmd/pickup-container", s.protect(http.HandlerFunc(s.handlePickupContainer)))
	s.mux.Handle("POST /api/cmd/mine", s.protect(http.HandlerFunc(s.handleMine)))
	s.mux.Handle("POST /api/cmd/install-satellite", s.protect(http.HandlerFunc(s.handleInstallSatellite)))
	s.mux.Handle("POST /api/ship/{id}/activate", s.protect(http.HandlerFunc(s.handleActivateShip)))
	s.mux.Handle("GET /api/player/ships", s.protect(http.HandlerFunc(s.handleFleet)))
	s.mux.Handle("POST /api/cmd/cargo/move", s.protect(http.HandlerFunc(s.handleCargoMove)))
	s.mux.HandleFunc("GET /api/ship/{id}/cargo", s.handleCargoInventory(domain.EntityKindShip))
	// Station holds are scoped to the requester's own goods (phase 10.22), so
	// these reads must run under auth to populate the player id.
	s.mux.Handle("GET /api/station/{id}/cargo", s.protect(s.handleCargoInventory(domain.EntityKindStation)))
	s.mux.Handle("GET /api/trade-station/{id}/cargo", s.protect(s.handleCargoInventory(domain.EntityKindTradeStation)))
	// Market reads are dock-gated (phase 10.3.12): the handler resolves the
	// player's active ship, so they must run under auth to populate the player id.
	s.mux.Handle("GET /api/station/{id}/market", s.protect(s.handleMarket(domain.EntityKindStation)))
	s.mux.Handle("GET /api/trade-station/{id}/market", s.protect(s.handleMarket(domain.EntityKindTradeStation)))
	s.mux.Handle("GET /api/pirbase/{id}/market", s.protect(s.handleMarket(domain.EntityKindPirbase)))
	s.mux.Handle("GET /api/market-scan", s.protect(http.HandlerFunc(s.handleMarketScan)))
	s.mux.Handle("POST /api/cmd/trade/buy", s.protect(http.HandlerFunc(s.handleTradeBuy)))
	s.mux.Handle("POST /api/cmd/trade/sell", s.protect(http.HandlerFunc(s.handleTradeSell)))
	s.mux.HandleFunc("GET /api/goods", s.handleGoods)
	s.mux.HandleFunc("GET /api/ship-classes", s.handleShipClasses)
	s.mux.HandleFunc("GET /api/station-types", s.handleStationTypes)
	s.mux.HandleFunc("GET /api/equipment", s.handleEquipment)
	s.mux.HandleFunc("GET /api/races", s.handleRaces)
	s.mux.HandleFunc("GET /api/auction", s.handleAuctionList)
	s.mux.Handle("GET /api/auction/mine", s.protect(http.HandlerFunc(s.handleAuctionMine)))
	s.mux.Handle("POST /api/auction", s.protect(http.HandlerFunc(s.handleAuctionCreate)))
	s.mux.Handle("POST /api/auction/{id}/bid", s.protect(http.HandlerFunc(s.handleAuctionBid)))
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("GET /api/world", s.handleWorld)
	s.mux.Handle("GET /ws", s.protect(http.HandlerFunc(s.handleWS)))

	return s
}

// Mux exposes the underlying ServeMux so other modules (e.g. auth) can
// register additional routes on the same handler tree.
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) protect(h http.Handler) http.Handler {
	if s.cfg.AuthMiddleware == nil {
		return h
	}
	return s.cfg.AuthMiddleware(h)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

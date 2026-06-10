// Package quest is the quest engine. Phase 8.12 shipped the polled MVP
// (dock/acquire_cargo/earn_cash, lazy-started tutorial). Phase 8.17 extends it
// into a scripted framework: a v2 step taxonomy advanced by a hybrid of
// polling (boolean state: dock/acquire/earn/goto/dock_at) and discrete domain
// events (counters: kill/deliver/trade), a state machine with
// failed/abandoned, per-quest deadlines, prerequisite chains, and
// accept/abandon. See back/docs/specs/quest.md.
package quest

import (
	"time"

	"spaceempire/back/internal/domain"
)

// StepKind is a quest step's completion condition. Polled kinds are checked
// against a player Snapshot each poller tick; event kinds accumulate a counter
// from domain events (see Step.MatchEvent).
type StepKind string

const (
	// --- polled (boolean over Snapshot) ---
	StepDock         StepKind = "dock"          // docked at any station
	StepAcquireCargo StepKind = "acquire_cargo" // hold ≥ Qty units
	StepEarnCash     StepKind = "earn_cash"     // cash ≥ Amount
	StepGotoSector   StepKind = "goto_sector"   // ship is in Sector
	StepDockAt       StepKind = "dock_at"       // docked at Target
	// --- event (counter toward Count) ---
	StepKill    StepKind = "kill"    // killed Count ships (or the linked Target ship)
	StepDeliver StepKind = "deliver" // delivered Count units of Goods to Target
	StepTrade   StepKind = "trade"   // traded Count units of Goods on Side (buy/sell)
	// --- polled survival timer; fails on escortee death (phase 8.18) ---
	StepEscortSurvive StepKind = "escort_survive" // escortee (TargetRole) survives Count poller ticks
)

// Step is one objective in a quest. RewardCash is granted when the step is
// satisfied (and the quest advances past it). Count is the event-step target.
type Step struct {
	Kind   StepKind
	Qty    int64            // StepAcquireCargo
	Amount int64            // StepEarnCash
	Count  int64            // StepKill/StepDeliver/StepTrade target / StepEscortSurvive ticks (≥1)
	Sector domain.SectorID  // StepGotoSector
	Target domain.EntityRef // StepDockAt / StepDeliver dest (Kind 0 = any)
	// TargetRole binds a kill/escort step to the quest's spawned NPCs of that
	// role (phase 8.18) instead of a static Target: a kill's goal becomes the
	// number spawned, and only those victims count; escort tracks that ship.
	TargetRole string
	Goods      domain.GoodsTypeID
	Side       string // StepTrade: "buy" / "sell" ("" = either)
	RewardCash int64
	Desc       string
}

// QuestSpawn is one NPC group a quest spawns on accept (phase 8.18): Count
// ships of Race in Sector, owned by the system player and driven by the race
// controller. FromGate spawns at a gate exit into Sector (a siege breaking
// through) rather than the sector centre. Role links the spawned ships to the
// kill/escort step that references them.
type QuestSpawn struct {
	Role     string
	Race     domain.RaceID
	Sector   domain.SectorID
	Count    int
	FromGate bool
}

// Def is a quest definition: an ordered list of steps, optionally with a
// prerequisite quest (chains), a deadline (relative to accept; zero = none),
// NPC spawns resolved on accept, and an Offerable flag (player-acceptable vs
// auto-started like the tutorial).
type Def struct {
	ID           string
	Title        string
	Steps        []Step
	Prerequisite string        // quest id that must be completed first ("" = none)
	Deadline     time.Duration // 0 = no deadline
	Offerable    bool          // true = accept/abandon; false = system-managed
	Spawns       []QuestSpawn  // NPCs spawned on accept (phase 8.18)
}

// EventDriven reports whether the step advances from domain events (counter)
// rather than polled snapshot state.
func (s Step) EventDriven() bool {
	switch s.Kind {
	case StepKill, StepDeliver, StepTrade:
		return true
	default:
		return false
	}
}

// Goal is the event-step target count (≥1; defaults to 1 when unset).
func (s Step) Goal() int64 {
	if s.Count <= 0 {
		return 1
	}
	return s.Count
}

// Snapshot is the slice of a player's live state the poller reads each tick.
type Snapshot struct {
	Docked        bool
	CargoUnits    int64
	Cash          int64
	CurrentSector domain.SectorID
	DockedTarget  domain.EntityRef // Kind 0 when not docked
}

// Satisfied reports whether a polled step's condition holds for the snapshot.
// Event-driven steps always return false here — they advance via MatchEvent.
func (s Step) Satisfied(snap Snapshot) bool {
	switch s.Kind {
	case StepDock:
		return snap.Docked
	case StepAcquireCargo:
		return snap.CargoUnits >= s.Qty
	case StepEarnCash:
		return snap.Cash >= s.Amount
	case StepGotoSector:
		return snap.CurrentSector == s.Sector
	case StepDockAt:
		return snap.Docked && snap.DockedTarget == s.Target
	default:
		return false
	}
}

// MatchEvent reports the counter delta an event contributes to this step, and
// whether the event is relevant at all. Only event-driven steps match.
func (s Step) MatchEvent(ev Event) (delta int64, ok bool) {
	switch s.Kind {
	case StepKill:
		if ev.Kind != EventKill {
			return 0, false
		}
		// A target-bound kill (e.g. a quest NPC) only counts for that victim.
		if s.Target.Kind != domain.EntityKindUnknown && ev.Victim != s.Target {
			return 0, false
		}
		return 1, true
	case StepDeliver:
		if ev.Kind != EventDeliver || ev.Goods != s.Goods {
			return 0, false
		}
		if s.Target.Kind != domain.EntityKindUnknown && ev.Target != s.Target {
			return 0, false
		}
		return ev.Amount, true
	case StepTrade:
		if ev.Kind != EventTrade || ev.Goods != s.Goods {
			return 0, false
		}
		if s.Side != "" && ev.Side != s.Side {
			return 0, false
		}
		return ev.Amount, true
	default:
		// escort_survive and target-bound kills are victim-scoped (handled by
		// Service.OnShipDestroyed, not killer-scoped OnEvent), so they never
		// match here.
		return 0, false
	}
}

// TutorialID is the starter quest every new player gets (auto-started).
const TutorialID = "tutorial"

// Tutorial teaches the core loop: dock → buy cargo → trade up.
var Tutorial = Def{
	ID:    TutorialID,
	Title: "Первые шаги",
	Steps: []Step{
		{Kind: StepDock, RewardCash: 500, Desc: "Пристыкуйся к станции"},
		{Kind: StepAcquireCargo, Qty: 1, RewardCash: 500, Desc: "Купи товар (1 ед. в трюме)"},
		{Kind: StepEarnCash, Amount: 15000, RewardCash: 5000, Desc: "Накопи 15 000 кр. (продай товар)"},
	},
}

// Demo offerable quests (phase 8.17) exercise the v2 framework; real X-Tension
// microquests land in 8.18. They are opt-in (Offerable) so they never disturb
// a player who doesn't accept them.
var (
	// Patrol — a deadline-bound bounty: kill 2 ships within a day.
	Patrol = Def{
		ID: "patrol", Title: "Патруль сектора", Offerable: true, Deadline: 24 * time.Hour,
		Steps: []Step{
			{Kind: StepKill, Count: 2, RewardCash: 4000, Desc: "Уничтожь 2 корабля"},
		},
	}
	// Saga part 1 → part 2 (chain): part 2 is blocked until part 1 completes.
	Saga1 = Def{
		ID: "saga1", Title: "Сага: пролог", Offerable: true,
		Steps: []Step{{Kind: StepEarnCash, Amount: 20000, RewardCash: 1000, Desc: "Накопи 20 000 кр."}},
	}
	Saga2 = Def{
		ID: "saga2", Title: "Сага: финал", Offerable: true, Prerequisite: "saga1",
		Steps: []Step{{Kind: StepGotoSector, Sector: 1, RewardCash: 5000, Desc: "Прибудь в сектор #1"}},
	}
)

// X-Tension microquests (phase 8.18 vertical slice). These exercise the
// quest-NPC spawn lifecycle: each spawns NPCs on accept, binds a kill/escort
// step to them by role, and despawns them on a terminal transition. Targets
// are adapted to the numeric world (sector 1 = Argon Prime, populated + gated);
// original names are kept as flavour.
var (
	// 6000800: a lone target spawned in the sector, killed by the player.
	KillFugitive = Def{
		ID: "xt_6008000", Title: "Убить беглого убийцу", Offerable: true,
		Spawns: []QuestSpawn{{Role: "target", Race: 6, Sector: 1, Count: 1}},
		Steps: []Step{
			{Kind: StepGotoSector, Sector: 1, Desc: "Прибудь в сектор #1"},
			{Kind: StepKill, TargetRole: "target", RewardCash: 5000, Desc: "Уничтожь беглого убийцу"},
		},
	}
	// 6002100: a Xenon breakthrough — the wave spawns at a gate exit and flies
	// inward; kill all of them.
	Siege = Def{
		ID: "xt_6002100", Title: "Небольшая осада", Offerable: true,
		Spawns: []QuestSpawn{{Role: "enemy", Race: 7, Sector: 1, Count: 3, FromGate: true}},
		Steps: []Step{
			{Kind: StepGotoSector, Sector: 1, Desc: "Прибудь в сектор #1"},
			{Kind: StepKill, TargetRole: "enemy", RewardCash: 30000, Desc: "Отрази ксенонский прорыв"},
		},
	}
	// 6002300: defend an attacked trader — keep the escortee alive while pirates
	// press it. Count is survived poller ticks (≈ ticks × poller interval).
	EscortTrader = Def{
		ID: "xt_6002300", Title: "Атакованный торговец", Offerable: true,
		Spawns: []QuestSpawn{
			{Role: "escortee", Race: 1, Sector: 1, Count: 1},
			{Role: "enemy", Race: 6, Sector: 1, Count: 2},
		},
		Steps: []Step{
			{Kind: StepEscortSurvive, TargetRole: "escortee", Count: 12, RewardCash: 5000, Desc: "Защити торговца — продержись"},
		},
	}
)

// X-Tension microquests (phase 8.18 second increment) — the remaining 10, on
// the same engine. World adaptation: named stations become "any station"
// (deliver/dock with no Target), Spaceweed/medicine become catalogue goods
// (Space Fuel 40, Computer Parts 5, Microchips 7, Crystals 4); sectors are
// numeric (1 = Argon Prime, 5 = Boron capital). Original names kept as flavour.
var (
	// 6000200: clear a pirate raid that landed in the sector.
	CombatMission1 = Def{
		ID: "xt_6000200", Title: "Комплексная боевая миссия 1", Offerable: true,
		Spawns: []QuestSpawn{{Role: "enemy", Race: 6, Sector: 1, Count: 3}},
		Steps: []Step{
			{Kind: StepGotoSector, Sector: 1, Desc: "Прибудь в сектор #1"},
			{Kind: StepKill, TargetRole: "enemy", RewardCash: 20000, Desc: "Уничтожь пиратскую группу"},
		},
	}
	// 6000300: rush a fuel + supplies run before the deadline.
	SickPrincess = Def{
		ID: "xt_6000300", Title: "Больная принцесса", Offerable: true, Deadline: 24 * time.Hour,
		Steps: []Step{
			{Kind: StepAcquireCargo, Qty: 13, Desc: "Загрузи 13 ед. груза"},
			{Kind: StepDeliver, Goods: 40, Count: 13, RewardCash: 25000, Desc: "Доставь 13 Космотоплива на станцию"},
		},
	}
	// 6000400: pick up a package, run it to sector 5 — Xenon ambush waits there.
	DeliverPackage = Def{
		ID: "xt_6000400", Title: "Доставь посылку", Offerable: true,
		Spawns: []QuestSpawn{{Role: "ambush", Race: 7, Sector: 5, Count: 2}},
		Steps: []Step{
			{Kind: StepDock, Desc: "Пристыкуйся к станции (забери посылку)"},
			{Kind: StepGotoSector, Sector: 5, Desc: "Доберись до сектора #5"},
			{Kind: StepDeliver, Goods: 5, Count: 1, RewardCash: 4500, Desc: "Доставь посылку (1 Computer Parts)"},
		},
	}
	// 6002500: Syndicate Sakra (part 1) — find their contact and take out a goon.
	SakraSyndicate = Def{
		ID: "xt_6002500", Title: "Синдикат Sakra (ч.1)", Offerable: true,
		Spawns: []QuestSpawn{{Role: "goon", Race: 6, Sector: 1, Count: 1}},
		Steps: []Step{
			{Kind: StepDock, Desc: "Свяжись с контактом (пристыкуйся)"},
			{Kind: StepKill, TargetRole: "goon", RewardCash: 8000, Desc: "Устрани головореза Sakra"},
		},
	}
	// 6004900: passenger run A → sector 5 → B within the deadline. Passenger
	// board/drop on the player ship is not modelled yet — docking endpoints
	// stand in for pickup/dropoff (adaptation).
	PassengerRun = Def{
		ID: "xt_6004900", Title: "Пассажирские перевозки", Offerable: true, Deadline: 7 * time.Hour,
		Steps: []Step{
			{Kind: StepDock, Desc: "Прими пассажиров (пристыкуйся)"},
			{Kind: StepGotoSector, Sector: 5, Desc: "Доставь их в сектор #5"},
			{Kind: StepDock, RewardCash: 200000, Desc: "Высади пассажиров (пристыкуйся)"},
		},
	}
	// 6006200: industrial sabotage, part 1 — plant a device on a target station.
	Sabotage1 = Def{
		ID: "xt_6006200", Title: "Промышленный саботаж (ч.1)", Offerable: true,
		Steps: []Step{
			{Kind: StepGotoSector, Sector: 1, Desc: "Прибудь в сектор #1"},
			{Kind: StepDock, Desc: "Проникни на станцию-цель (пристыкуйся)"},
			{Kind: StepDeliver, Goods: 5, Count: 1, RewardCash: 2000, Desc: "Заложи устройство (1 Computer Parts)"},
		},
	}
	// 6006300: sabotage part 2 — blocked until part 1 completes.
	Sabotage2 = Def{
		ID: "xt_6006300", Title: "Промышленный саботаж (ч.2)", Offerable: true, Prerequisite: "xt_6006200",
		Steps: []Step{
			{Kind: StepGotoSector, Sector: 5, Desc: "Доберись до сектора #5"},
			{Kind: StepDeliver, Goods: 5, Count: 1, RewardCash: 50000, Desc: "Заверши саботаж (1 Computer Parts)"},
		},
	}
	// 6008500: help the Teladi with an Argon-Flu outbreak (medicine → goods sub).
	ArgonFlu = Def{
		ID: "xt_6008500", Title: "Помочь Телади (Argon Flu)", Offerable: true, Deadline: 24 * time.Hour,
		Steps: []Step{
			{Kind: StepAcquireCargo, Qty: 5, Desc: "Закупи медикаменты (5 ед. груза)"},
			{Kind: StepDeliver, Goods: 7, Count: 5, RewardCash: 30000, Desc: "Доставь 5 Микрочипов (медикаменты)"},
		},
	}
	// 6009000: protect a transport — friendly fighters help against the pirates.
	ProtectTransport = Def{
		ID: "xt_6009000", Title: "Защитить транспорт", Offerable: true,
		Spawns: []QuestSpawn{
			{Role: "escortee", Race: 1, Sector: 1, Count: 1},
			{Role: "ally", Race: 1, Sector: 1, Count: 2},
			{Role: "enemy", Race: 6, Sector: 1, Count: 3},
		},
		Steps: []Step{
			{Kind: StepEscortSurvive, TargetRole: "escortee", Count: 16, RewardCash: 5000, Desc: "Защити транспорт — продержись"},
		},
	}
	// 6009700: complex trade — buy low, ship it, sell high.
	ComplexTrade = Def{
		ID: "xt_6009700", Title: "Сложная торговля", Offerable: true,
		Steps: []Step{
			{Kind: StepTrade, Side: "buy", Goods: 4, Count: 5, Desc: "Купи 5 Кристаллов"},
			{Kind: StepGotoSector, Sector: 5, Desc: "Доберись до сектора #5"},
			{Kind: StepTrade, Side: "sell", Goods: 4, Count: 5, RewardCash: 6000, Desc: "Продай 5 Кристаллов"},
		},
	}
)

// microquests is the X-Tension 8.18 catalogue (slice + second increment),
// listed in offer order.
var microquests = []Def{
	KillFugitive, Siege, EscortTrader, // slice
	CombatMission1, SickPrincess, DeliverPackage, SakraSyndicate, PassengerRun,
	Sabotage1, Sabotage2, ArgonFlu, ProtectTransport, ComplexTrade,
}

// defs is the registry of known quests, keyed by id.
var defs = func() map[string]Def {
	m := map[string]Def{
		TutorialID: Tutorial,
		Patrol.ID:  Patrol,
		Saga1.ID:   Saga1,
		Saga2.ID:   Saga2,
	}
	for _, d := range microquests {
		m[d.ID] = d
	}
	return m
}()

// Lookup returns a quest definition by id.
func Lookup(id string) (Def, bool) {
	d, ok := defs[id]
	return d, ok
}

// Offerable returns the quests a player can accept, in a stable order: the
// 8.17 demos first, then the X-Tension microquest catalogue (8.18).
func Offerable() []Def {
	out := make([]Def, 0, 3+len(microquests))
	for _, id := range []string{Patrol.ID, Saga1.ID, Saga2.ID} {
		if d, ok := defs[id]; ok && d.Offerable {
			out = append(out, d)
		}
	}
	for _, d := range microquests {
		if d.Offerable {
			out = append(out, d)
		}
	}
	return out
}

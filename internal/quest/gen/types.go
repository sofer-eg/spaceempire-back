// Package gen is the procedural quest generator (TASK-89, SRS FR-01/02/03/09).
// It turns the static quest templates into concrete quest.Def instances whose
// target sectors and traded goods are chosen at generation time from what is
// reachable and tradeable around the player, never hard-coded.
//
// The generator is pure and deterministic: given a fixed *rand.Rand, router,
// market and statics, Generate returns the same Def every time. All map
// iteration is sorted before an rng pick so the output does not depend on Go's
// randomised map order. Dependencies are injected as minimal ISP interfaces so
// unit tests drive it with fakes.
//
// Scope note (cross-MR): this MR builds the generator only. The pacer that
// decides WHEN to offer, the offer persistence and WS delivery, and the app
// wiring land in later MRs. The generated Def.ID is left as the template kind;
// the final proc:<offer_id> id is assigned at accept-materialization (MR5),
// once the offer's BIGSERIAL id is known — the generator never sets it.
package gen

import (
	"context"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// Template kinds. These double as the generated Def.ID and as the offer's
// template_id column value for procedural offers (migration 0060).
const (
	TemplateDeliver = "deliver"
	TemplateTrade   = "trade"
	TemplateKill    = "kill"
	TemplateCourier = "courier"
)

// Per-template quantity/count ranges (inclusive). These are the template's own
// "difficulty knobs" (FR-01) and live in code, not QuestConfig: QuestConfig
// carries the pacing/reward tuning, the shape of a template is code. Grounded
// in the X-Tension catalogue (delivers ~5-13 units, patrols kill 2-3).
const (
	deliverQtyMin, deliverQtyMax = 5, 15
	tradeQtyMin, tradeQtyMax     = 5, 15
	killCountMin, killCountMax   = 2, 4
)

// PathRouter resolves gate-hop distance between sectors. Satisfied by
// *world.PathRouter and the fake in tests. Hops returns (n, true) for a
// reachable destination, (0, false) when unreachable or excluded.
type PathRouter interface {
	Hops(from, to domain.SectorID) (int, bool)
}

// MarketListing is one tradeable position at a dockable station in a reachable
// sector, projected from the player's point of view: CanBuy means the station
// sells the good and has stock (the player can buy it here), CanSell means the
// station buys the good (the player can sell it here).
type MarketListing struct {
	Station domain.EntityRef
	Sector  domain.SectorID
	Goods   domain.GoodsTypeID
	CanBuy  bool
	CanSell bool
}

// Market reports what is tradeable across a set of sectors. The generator asks
// only about sectors it already knows are reachable, so it stays ignorant of
// owner resolution and the trade row shape. StaticMarket is the real adapter.
type Market interface {
	Listings(ctx context.Context, sectors []domain.SectorID) ([]MarketListing, error)
}

// GoodsCatalog resolves a goods id to its display row, for human-readable quest
// text (FR-11). Satisfied by *balance.Balance.
type GoodsCatalog interface {
	Get(id domain.GoodsTypeID) (balance.Goods, bool)
}

// Config is the generator's slice of QuestConfig (SRS §7.5). The app maps
// config.QuestConfig onto it so the gen package does not depend on pkg/config.
type Config struct {
	// TargetRadius bounds the gate-hop distance of a quest's target sectors.
	TargetRadius int
	// GoodsRadius bounds the gate-hop distance of a tradeable goods buy point.
	GoodsRadius int
	// Reward coefficients (FR-09): reward = base + perHop*hops + perUnit*qty
	// (+ perEnemy*count for kill), floored at base.
	RewardBase     int64
	RewardPerHop   int64
	RewardPerUnit  int64
	RewardPerEnemy int64
}

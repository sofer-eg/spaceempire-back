package domain

// GoodsTypeID identifies a row in the goods_types reference table.
type GoodsTypeID int32

// GoodsType describes one tradeable item kind. Space is the cubic-meter
// footprint of a single unit; total cargo usage of an inventory is the
// sum of Quantity*Space across its items.
type GoodsType struct {
	ID    GoodsTypeID
	Name  string
	Space float64
}

// Ammunition goods ids, declared here — in the leaf package every layer already
// imports — because they are the one piece of catalog data that three unrelated
// layers must agree on: app seeds the starter magazine, api spends a launch, and
// sector's kill drop treats a missile stack specially. They were separate
// constants until TASK-167, and the copies drifted: the consolidation moved api
// and app onto the real catalog and left sector behind on the deleted id 50, which
// silently turned the SP's probabilistic missile throw into dead code. One
// declaration makes that class of divergence a compile-time impossibility rather
// than something a test has to notice.
//
// The values are the legacy schema's own class→goods mapping, ct_missiles.cargo_id
// and ct_drones.cargo_id — the same table torpedos (23/24), the satellite (26) and
// the jammer (27) already come from. Only class 1 of each is wired: phase 4.3 flies
// exactly one missile spec (combat.DefaultMissileSpec), so 11-14 wait for a
// per-class spec catalog.
//
// These are ids, not the catalog: name/space still come from goods_types, which
// TestIntegration_BalanceCatalog_MatchesGoodsTypesTable pins to configs/balance.yaml.
// Keeping them here is what lets sector stay free of the balance package.
const (
	// MissileGoodsType is «Ракета Москит», the catalog's class-1 missile.
	MissileGoodsType GoodsTypeID = 10
	// DroneGoodsType is «Боевой дрон» (space 290 — a big-ship weapon).
	DroneGoodsType GoodsTypeID = 21
)

// CargoItem is one stack of goods owned by an entity (ship, station,
// trade station, …). The owner is implicit — callers always query items
// for a known EntityRef, so storing the owner inside every item would
// just duplicate context.
type CargoItem struct {
	GoodsType GoodsTypeID
	Quantity  int64
}

// Inventory is the full cargo snapshot for one owner, including the
// owner's capacity in space units and how much of it is currently used.
type Inventory struct {
	Owner    EntityRef
	Capacity float64
	Used     float64
	Items    []CargoItem
}

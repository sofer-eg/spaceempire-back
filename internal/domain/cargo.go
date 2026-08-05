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
// the jammer (27) already come from. All five missile classes are wired
// (TASK-175); drones still have exactly one class.
//
// These are ids, not the catalog: name/space still come from goods_types, which
// TestIntegration_BalanceCatalog_MatchesGoodsTypesTable pins to configs/balance.yaml.
// Keeping them here is what lets sector stay free of the balance package.
const (
	// MissileGoodsType is «Ракета Москит», the catalog's class-1 missile. It is
	// also the starter magazine app seeds, which is why it keeps the unqualified
	// name while classes 2-5 are spelled out below.
	MissileGoodsType GoodsTypeID = 10
	// MissileOsaGoodsType is «Ракета Оса», class 2 (space 1).
	MissileOsaGoodsType GoodsTypeID = 11
	// MissileStrekozaGoodsType is «Ракета Стрекоза», class 3 (space 1).
	MissileStrekozaGoodsType GoodsTypeID = 12
	// MissileShelkopryadGoodsType is «Ракета Шелкопряд», class 4 (space 2).
	MissileShelkopryadGoodsType GoodsTypeID = 13
	// MissileShershenGoodsType is «Ракета Шершень», class 5 (space 3).
	MissileShershenGoodsType GoodsTypeID = 14
	// DroneGoodsType is «Боевой дрон» (space 290 — a big-ship weapon).
	DroneGoodsType GoodsTypeID = 21
)

// MissileGoodsTypes returns every missile ammunition id, class 1 to 5 in order.
//
// It exists because the kill drop treats a missile stack as a CLASS of item, not
// as one good: SP KillObject's `cargo_missiles` cursor selects cargo by
// `inner join ct_missiles ctm on ctm.cargo_id = c.type`, so all five ids go
// through the probabilistic throw. Wiring classes 2-5 as launchable without this
// would have quietly split the rule — a Москит stack burning up on a wreck while
// a Шершень stack, worth thirty times as much, dropped in full every time.
func MissileGoodsTypes() []GoodsTypeID {
	return []GoodsTypeID{
		MissileGoodsType,
		MissileOsaGoodsType,
		MissileStrekozaGoodsType,
		MissileShelkopryadGoodsType,
		MissileShershenGoodsType,
	}
}

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

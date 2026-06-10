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

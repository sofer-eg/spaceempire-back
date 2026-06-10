package domain

// Rect is an axis-aligned bounding box used for sector extents. Min is the
// inclusive lower-left corner, Max the inclusive upper-right corner.
type Rect struct {
	Min Vec2
	Max Vec2
}

// Sector is one cell of the world map. Ships live in exactly one sector
// at a time; movement between sectors happens through Gates.
type Sector struct {
	ID     SectorID
	Name   string
	Bounds Rect
	// GridX, GridY are the galactic grid coordinates (StarWind pos_x/pos_y).
	// They place the sector on the schematic galaxy map; gates between
	// adjacent cells render as orthogonal connectors. Static — loaded once.
	GridX int
	GridY int
	// Race is the controlling faction of the sector, snapshot at load from
	// its built trade station (falling back to shipyard, then station). 0 is
	// neutral. Display-only: drives the galaxy-map sector colour.
	Race int
}

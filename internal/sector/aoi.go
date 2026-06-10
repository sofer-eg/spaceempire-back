package sector

import (
	"math"

	"spaceempire/back/internal/domain"
)

// aoiCellFactor multiplies AOIRadius to size the uniform grid cell. 1.5 is
// the sweet spot from the design (section 3): cells big enough that any
// query touches at most 4 cells in each direction, small enough that very
// crowded sectors still partition into manageable lists.
const aoiCellFactor = 1.5

// spatialGrid is the per-tick uniform-grid index used by AOI broadcasts.
// Cells hold pointers to the live sectorState ships, so the caller must
// finish using the grid before mutating those ships in the same tick — in
// practice the worker rebuilds the grid after movement and queries it
// before the next tick.
type spatialGrid struct {
	cellSize float64
	cells    map[gridKey][]*domain.Ship
}

type gridKey struct {
	x, y int
}

func newSpatialGrid(cellSize float64) *spatialGrid {
	if cellSize <= 0 {
		cellSize = 1
	}
	return &spatialGrid{
		cellSize: cellSize,
		cells:    make(map[gridKey][]*domain.Ship),
	}
}

func (g *spatialGrid) cellOf(p domain.Vec2) gridKey {
	return gridKey{
		x: int(math.Floor(p.X / g.cellSize)),
		y: int(math.Floor(p.Y / g.cellSize)),
	}
}

func (g *spatialGrid) insert(ship *domain.Ship) {
	k := g.cellOf(ship.Pos)
	g.cells[k] = append(g.cells[k], ship)
}

// queryIDs returns the set of ship IDs within radius of center. The bounding
// box of cells covers the circle conservatively (square inscribed around the
// circle); ships outside the radius are then filtered by exact distance.
func (g *spatialGrid) queryIDs(center domain.Vec2, radius float64) map[domain.ShipID]struct{} {
	out := make(map[domain.ShipID]struct{})
	if radius <= 0 {
		return out
	}
	r2 := radius * radius
	minX := int(math.Floor((center.X - radius) / g.cellSize))
	maxX := int(math.Floor((center.X + radius) / g.cellSize))
	minY := int(math.Floor((center.Y - radius) / g.cellSize))
	maxY := int(math.Floor((center.Y + radius) / g.cellSize))
	for cx := minX; cx <= maxX; cx++ {
		for cy := minY; cy <= maxY; cy++ {
			for _, ship := range g.cells[gridKey{cx, cy}] {
				dx := ship.Pos.X - center.X
				dy := ship.Pos.Y - center.Y
				if dx*dx+dy*dy <= r2 {
					out[ship.ID] = struct{}{}
				}
			}
		}
	}
	return out
}

// buildGrid creates a fresh grid populated with every ship in the sector.
// Called once per tick before broadcastPatches; rebuilding is cheap relative
// to the per-subscriber query work.
func buildGrid(ships map[domain.ShipID]*domain.Ship, cellSize float64) *spatialGrid {
	g := newSpatialGrid(cellSize)
	for _, s := range ships {
		g.insert(s)
	}
	return g
}

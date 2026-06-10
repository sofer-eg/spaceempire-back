package dto

import "spaceempire/back/internal/domain"

type WorldResponse struct {
	Sectors []Sector `json:"sectors"`
	Gates   []Gate   `json:"gates"`
}

type Sector struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Bounds Rect   `json:"bounds"`
	// GridX, GridY place the sector on the schematic galaxy map; Race tints it.
	GridX int `json:"gridX"`
	GridY int `json:"gridY"`
	Race  int `json:"race"`
}

type Rect struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

type Gate struct {
	ID      int64   `json:"id"`
	SectorA int64   `json:"sectorA"`
	PosAX   float64 `json:"posAX"`
	PosAY   float64 `json:"posAY"`
	SectorB int64   `json:"sectorB"`
	PosBX   float64 `json:"posBX"`
	PosBY   float64 `json:"posBY"`
}

func WorldFromDomain(sectors []domain.Sector, gates []domain.Gate) WorldResponse {
	out := WorldResponse{
		Sectors: make([]Sector, len(sectors)),
		Gates:   make([]Gate, len(gates)),
	}
	for i, s := range sectors {
		out.Sectors[i] = Sector{
			ID:   int64(s.ID),
			Name: s.Name,
			Bounds: Rect{
				MinX: s.Bounds.Min.X,
				MinY: s.Bounds.Min.Y,
				MaxX: s.Bounds.Max.X,
				MaxY: s.Bounds.Max.Y,
			},
			GridX: s.GridX,
			GridY: s.GridY,
			Race:  s.Race,
		}
	}
	for i, g := range gates {
		out.Gates[i] = Gate{
			ID:      int64(g.ID),
			SectorA: int64(g.SectorA),
			PosAX:   g.PosA.X,
			PosAY:   g.PosA.Y,
			SectorB: int64(g.SectorB),
			PosBX:   g.PosB.X,
			PosBY:   g.PosB.Y,
		}
	}
	return out
}

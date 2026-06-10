package dto

// ShipClass is one row of the static ship-class catalog returned by
// GET /api/ship-classes. Mirrors balance.ShipClass plus the derived
// category/label so the SPA can group ships without re-deriving them.
type ShipClass struct {
	ID            int64   `json:"id"`
	Race          int     `json:"race"`
	Type          int     `json:"type"`
	Class         int     `json:"class"`
	Category      string  `json:"category"`      // M1/M2/.../TS/XX
	CategoryLabel string  `json:"categoryLabel"` // Носитель/Эсминец/...
	Name          string  `json:"name"`
	Speed         float64 `json:"speed"`
	Acceleration  float64 `json:"acceleration"`
	Laser         int     `json:"laser"`
	Shield        int     `json:"shield"`
	Hull          int     `json:"hull"`
	CargoBay      int     `json:"cargobay"`
	BasePrice     int64   `json:"basePrice"`
	PilotCabin    int     `json:"pilotCabin"`
}

// ShipClassListResponse is the body of GET /api/ship-classes.
type ShipClassListResponse struct {
	Items []ShipClass `json:"items"`
}

package dto

// Equipment is one row of the ship-equipment catalog returned by
// GET /api/equipment. Mirrors balance.Equipment for the (future) outfitting
// screen — names, prices, slot and dependency.
type Equipment struct {
	ID            int64  `json:"id"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	MaxLevel      int    `json:"maxLevel"`
	Race          int    `json:"race"`
	ShipClass     int    `json:"shipClass"`
	Price         int64  `json:"price"`
	PricePerLevel int64  `json:"pricePerLevel"`
	IsBase        bool   `json:"isBase"`
	Position      int    `json:"position"`
	Dependance    string `json:"dependance"`
	// Reputation thresholds to install this module (0 = no gate). The server
	// enforces them at install (balance.ResolveInstall → 422); the catalog
	// exposes them so the outfitting screen can pre-block by reputation.
	MinWarRate    int    `json:"minWarRate"`
	MinTradeRate  int    `json:"minTradeRate"`
	MinRaceRate   int    `json:"minRaceRate"`
	EnergyUseType string `json:"energyUseType"`
	EnergyUsage   int    `json:"energyUsage"`
}

// EquipmentListResponse is the body of GET /api/equipment.
type EquipmentListResponse struct {
	Items []Equipment `json:"items"`
}

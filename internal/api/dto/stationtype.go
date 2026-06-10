package dto

// StationType is one row of the station-type catalog returned by
// GET /api/station-types. Mirrors balance.StationType but exposes only what
// the SPA renders (names + faction), not the production recipe.
type StationType struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Race      int    `json:"race"`
	Kind      int    `json:"kind"`
	KindLabel string `json:"kindLabel"`
	Sellable  bool   `json:"sellable"`
}

// StationTypeListResponse is the body of GET /api/station-types.
type StationTypeListResponse struct {
	Items []StationType `json:"items"`
}

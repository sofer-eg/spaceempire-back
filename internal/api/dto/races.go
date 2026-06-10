package dto

// Race is one row of the static race reference returned by GET /api/races.
// Mirrors race.Def but exposes only the fields the SPA renders (colour +
// names); capital location stays server-side.
type Race struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	StateName string `json:"stateName"`
	Color     string `json:"color"`
}

// RaceListResponse is the body of GET /api/races.
type RaceListResponse struct {
	Items []Race `json:"items"`
}

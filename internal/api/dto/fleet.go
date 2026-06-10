package dto

// FleetResponse is the body of GET /api/player/ships (phase 10.14a) — every
// ship the player owns across all sectors. Ships reuse the snapshot Ship shape
// so the SPA's existing shipDisplayName / class-catalog labelling applies.
type FleetResponse struct {
	Ships []Ship `json:"ships"`
}

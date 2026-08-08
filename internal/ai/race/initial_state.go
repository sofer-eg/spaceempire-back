package race

import (
	"encoding/json"

	"spaceempire/back/internal/domain"
)

// NewInitialState builds the ai_state JSON for a freshly spawned race ship: it
// patrols the given anchor (its spawn station/pirbase) and carries its race for
// inspection. Hostility is keyed off the ship's own race field at runtime
// (raceMatrixTargeter), not this copy. Mirrors trader/miner/passenger
// NewInitialState so the cold-start spawner builds every controller_kind the
// same way.
func NewInitialState(raceID int, anchor domain.Vec2) ([]byte, error) {
	return json.Marshal(state{
		Race:      raceID,
		Order:     OrderPatrol,
		Anchor:    anchor,
		HasAnchor: true,
	})
}

// NewInitialStandaloneState is NewInitialState for a ship that must stay out
// of emergent squads (TASK-207): quest NPCs (escorted traders, protect
// targets) share the race controller kind but must not become wingmen of a
// same-race navy squad or follow its group retreat. The ship keeps the full
// solo behaviour: anchor patrol, engage with focus-fire, personal flee.
func NewInitialStandaloneState(raceID int, anchor domain.Vec2) ([]byte, error) {
	return json.Marshal(state{
		Race:       raceID,
		Order:      OrderPatrol,
		Anchor:     anchor,
		HasAnchor:  true,
		Standalone: true,
	})
}

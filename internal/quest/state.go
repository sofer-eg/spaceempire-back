package quest

import (
	"encoding/json"

	"spaceempire/back/internal/domain"
)

// questState is the per-quest JSONB payload (player_quests.state). Progress is
// the counter toward the current step (event hits for kill/deliver/trade, or
// survived poller ticks for escort_survive); it resets when the step advances.
// Spawned records the quest's spawned NPCs by role (phase 8.18) so kill/escort
// steps bind to them and despawn removes them on a terminal transition. Kept
// minimal and forward-compatible (unknown fields are dropped on decode).
type questState struct {
	Progress int64              `json:"p,omitempty"`
	Spawned  map[string][]int64 `json:"spawned,omitempty"`
}

// spawnedSet returns the spawned ship ids of a role as a lookup set.
func (s questState) spawnedSet(role string) map[int64]bool {
	ids := s.Spawned[role]
	if len(ids) == 0 {
		return nil
	}
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// allSpawned flattens every spawned ship id across roles (for despawn).
func (s questState) allSpawned() []domain.ShipID {
	var out []domain.ShipID
	for _, ids := range s.Spawned {
		for _, id := range ids {
			out = append(out, domain.ShipID(id))
		}
	}
	return out
}

func decodeState(b []byte) questState {
	var s questState
	if len(b) > 0 {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func encodeState(s questState) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return []byte("{}")
	}
	return b
}

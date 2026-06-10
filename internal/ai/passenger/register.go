package passenger

import (
	"encoding/json"
	"fmt"

	"spaceempire/back/internal/ai"
)

// Register wires the "passenger" controller kind into the registry. The
// shared Config is captured in the factory closure, so every passenger TS
// rebuilt from ai_state (cold-start or gate handoff) uses the same thresholds.
// Call once during app wiring.
func Register(registry *ai.Registry, cfg Config) {
	cfg = cfg.withDefaults()
	registry.Register(Kind, func(stateJSON []byte) (ai.Controller, error) {
		var st state
		if len(stateJSON) > 0 {
			if err := json.Unmarshal(stateJSON, &st); err != nil {
				return nil, fmt.Errorf("unmarshal passenger state: %w", err)
			}
		}
		if st.Phase == "" {
			st.Phase = PhaseWaiting
		}
		return &Controller{cfg: cfg, st: st}, nil
	})
}

// NewInitialState builds the ai_state StateJSON for a freshly spawned
// passenger TS: parked (waiting, no delay) at its home station, with a fixed
// pool of nearby destinations and a per-ship round-robin offset so co-located
// ships fan out rather than all heading to the same first stop. The NPC
// spawner (app) calls this.
func NewInitialState(home Leg, pool []Leg, routeIdx int) ([]byte, error) {
	return json.Marshal(state{
		Phase:    PhaseWaiting,
		Pool:     pool,
		Dest:     home,
		RouteIdx: routeIdx,
		WaitLeft: 0,
	})
}

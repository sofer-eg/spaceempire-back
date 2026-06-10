package miner

import (
	"encoding/json"
	"fmt"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// Register wires the "miner" controller kind into the registry. The shared
// Config is captured in the factory closure, so every miner rebuilt from
// ai_state (cold-start or gate handoff) uses the same thresholds. Call once
// during app wiring.
func Register(registry *ai.Registry, cfg Config) {
	cfg = cfg.withDefaults()
	registry.Register(Kind, func(stateJSON []byte) (ai.Controller, error) {
		var st state
		if len(stateJSON) > 0 {
			if err := json.Unmarshal(stateJSON, &st); err != nil {
				return nil, fmt.Errorf("unmarshal miner state: %w", err)
			}
		}
		if st.Phase == "" {
			// No target yet — start idle so the controller looks for an
			// asteroid in its sector rather than flying to a zero target.
			st.Phase = PhaseIdle
		}
		return &Controller{cfg: cfg, st: st}, nil
	})
}

// NewInitialState builds the ai_state StateJSON for a freshly spawned miner:
// phase to_asteroid, bound to a home factory, an ore type, and an initial
// target asteroid the spawner picked. The NPC spawner (app) calls this.
func NewInitialState(home Leg, ore domain.GoodsTypeID, target Target) ([]byte, error) {
	return json.Marshal(state{
		Phase:  PhaseToAsteroid,
		Home:   home,
		Ore:    ore,
		Target: target,
	})
}

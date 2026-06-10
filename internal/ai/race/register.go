package race

import (
	"encoding/json"
	"fmt"

	"spaceempire/back/internal/ai"
)

// Register wires the "race" controller kind into the registry. The shared
// Targeter and Config are captured in the factory closure, so every race
// controller rebuilt from ai_state resolves hostility the same way. Call
// once during app wiring (after the relations Service is ready).
func Register(registry *ai.Registry, targeter Targeter, cfg Config) {
	cfg = cfg.withDefaults()
	registry.Register(Kind, func(stateJSON []byte) (ai.Controller, error) {
		var st state
		if len(stateJSON) > 0 {
			if err := json.Unmarshal(stateJSON, &st); err != nil {
				return nil, fmt.Errorf("unmarshal race state: %w", err)
			}
		}
		if st.Order == "" {
			st.Order = OrderPatrol
		}
		return &Controller{cfg: cfg, targeter: targeter, st: st}, nil
	})
}

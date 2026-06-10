package trader

import (
	"encoding/json"
	"fmt"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// Register wires the "trader" controller kind into the registry. The shared
// Config is captured in the factory closure, so every trader rebuilt from
// ai_state (cold-start or gate handoff) uses the same thresholds. Call once
// during app wiring.
func Register(registry *ai.Registry, cfg Config) {
	cfg = cfg.withDefaults()
	registry.Register(Kind, func(stateJSON []byte) (ai.Controller, error) {
		var st state
		if len(stateJSON) > 0 {
			if err := json.Unmarshal(stateJSON, &st); err != nil {
				return nil, fmt.Errorf("unmarshal trader state: %w", err)
			}
		}
		if st.Phase == "" {
			st.Phase = PhaseHome
		}
		return &Controller{cfg: cfg, st: st}, nil
	})
}

// NewInitialState builds the ai_state StateJSON for a freshly spawned
// trader: phase home, with the given route and per-route haul cap. The NPC
// spawner (app) calls this to seed the trader's ai_state row.
func NewInitialState(home, dest Leg, goods domain.GoodsTypeID, haulQty int64) ([]byte, error) {
	return json.Marshal(state{
		Phase:   PhaseHome,
		Home:    home,
		Dest:    dest,
		Goods:   goods,
		HaulQty: haulQty,
	})
}

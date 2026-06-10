package ai

import (
	"errors"
	"fmt"
)

// ErrUnknownKind is returned by Registry.Build when no factory is
// registered for the requested controller kind. The worker logs and skips
// such a ship at cold-start (a stale ai_state row referencing a controller
// that no longer exists must not crash the load).
var ErrUnknownKind = errors.New("ai: unknown controller kind")

// Factory builds a Controller from its persisted state. state is the raw
// StateJSON from ai_state (nil/empty for a fresh controller). A factory
// must tolerate empty state by starting from its zero value.
type Factory func(state []byte) (Controller, error)

// Registry maps a controller_kind discriminator to the Factory that
// rebuilds it. The worker consults it only at cold-start, to hydrate
// controllers from ai_state rows. Built once during wiring (app.Run) and
// never mutated afterwards, so it needs no locking — Register is expected
// to run before any Build.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty Registry. Phase 5.1 wires it empty (no NPC
// controller kinds exist yet); 5.2+ register their controllers here.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register associates a controller kind with its Factory. A later Register
// for the same kind overwrites the earlier one.
func (r *Registry) Register(kind string, f Factory) {
	r.factories[kind] = f
}

// Build instantiates the controller for kind from its persisted state.
// Returns ErrUnknownKind (wrapped) when the kind is not registered.
func (r *Registry) Build(kind string, state []byte) (Controller, error) {
	f, ok := r.factories[kind]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	return f(state)
}

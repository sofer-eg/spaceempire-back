package ai_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// stubController is a minimal Controller for registry tests: it echoes the
// state bytes it was built from so the test can assert the factory received
// them.
type stubController struct {
	state []byte
}

func (c *stubController) Kind() string                                 { return "stub" }
func (c *stubController) Tick(context.Context, ai.WorldView) ai.Action { return ai.Idle{} }
func (c *stubController) MarshalState() ([]byte, error)                { return c.state, nil }

func TestUnit_Registry_BuildKnownKind(t *testing.T) {
	t.Parallel()

	r := ai.NewRegistry()
	r.Register("stub", func(state []byte) (ai.Controller, error) {
		return &stubController{state: state}, nil
	})

	ctrl, err := r.Build("stub", []byte(`{"phase":3}`))
	require.NoError(t, err)
	require.Equal(t, "stub", ctrl.Kind())

	got, err := ctrl.MarshalState()
	require.NoError(t, err)
	assert.JSONEq(t, `{"phase":3}`, string(got))
}

func TestUnit_Registry_BuildUnknownKind(t *testing.T) {
	t.Parallel()

	r := ai.NewRegistry()
	_, err := r.Build("missing", nil)
	assert.ErrorIs(t, err, ai.ErrUnknownKind)
}

// Action values must satisfy the closed Action interface so the worker can
// switch on them. A compile-time check that the variants implement it.
func TestUnit_Action_Variants(t *testing.T) {
	t.Parallel()

	var actions = []ai.Action{
		ai.Idle{},
		ai.MoveTo{Target: domain.Vec2{X: 1, Y: 2}},
	}
	require.Len(t, actions, 2)
}

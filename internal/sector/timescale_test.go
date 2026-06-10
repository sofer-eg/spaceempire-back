package sector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUnit_AdjustTimeScale_SlowsUnderOverload(t *testing.T) {
	t.Parallel()
	interval := time.Second

	// A single overloaded tick (1.5× interval) drops the scale by ×0.8.
	got := adjustTimeScale(1.0, 1500*time.Millisecond, interval)
	assert.InDelta(t, 0.8, got, 1e-9)

	// Sustained overload keeps dropping but never below the 0.1 floor.
	scale := 1.0
	for i := 0; i < 50; i++ {
		scale = adjustTimeScale(scale, 5*time.Second, interval)
	}
	assert.Equal(t, minTimeScale, scale, "clamped at the floor")
}

func TestUnit_AdjustTimeScale_RecoversWhenIdle(t *testing.T) {
	t.Parallel()
	interval := time.Second

	// Fast ticks (< 0.5× interval) raise the scale by ×1.2.
	got := adjustTimeScale(0.5, 100*time.Millisecond, interval)
	assert.InDelta(t, 0.6, got, 1e-9)

	// Sustained headroom recovers all the way to 1.0 and caps there.
	scale := minTimeScale
	for i := 0; i < 50; i++ {
		scale = adjustTimeScale(scale, 10*time.Millisecond, interval)
	}
	assert.Equal(t, 1.0, scale, "capped at real time")
}

func TestUnit_AdjustTimeScale_HoldsInDeadband(t *testing.T) {
	t.Parallel()
	interval := time.Second
	// Between 0.5× and 1.2× the interval the scale is unchanged.
	assert.Equal(t, 0.7, adjustTimeScale(0.7, 800*time.Millisecond, interval))
	assert.Equal(t, 1.0, adjustTimeScale(1.0, time.Second, interval))
}

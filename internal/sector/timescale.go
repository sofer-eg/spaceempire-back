package sector

import (
	"math"
	"time"
)

// minTimeScale is the floor the dilation never drops below — at 0.1 the sector
// still advances (1/10th speed) rather than freezing, which is the whole point
// of graceful degradation (keep TPS up, slow game time). See
// docs/specs/tidi.md (design §3, Eve-style Time Dilation).
const minTimeScale = 0.1

// timeScaleWarnThreshold is the dilation level below which the worker logs a
// warning (the sector is meaningfully overloaded).
const timeScaleWarnThreshold = 0.5

// adjustTimeScale returns the next time-scale given the current one and how
// long the last tick took relative to the interval:
//   - over 1.2× the interval → overloaded: slow down (×0.8, floored at 0.1)
//   - under 0.5× the interval → headroom: speed back up (×1.2, capped at 1.0)
//   - in between → hold steady
//
// Pure and clamped so it is trivially testable without a worker.
func adjustTimeScale(current float64, elapsed, interval time.Duration) float64 {
	if interval <= 0 {
		return 1.0
	}
	ratio := float64(elapsed) / float64(interval)
	switch {
	case ratio > 1.2:
		return math.Max(minTimeScale, current*0.8)
	case ratio < 0.5:
		return math.Min(1.0, current*1.2)
	default:
		return current
	}
}

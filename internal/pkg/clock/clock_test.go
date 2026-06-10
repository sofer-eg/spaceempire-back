package clock_test

import (
	"testing"
	"time"

	"spaceempire/back/internal/pkg/clock"
)

func TestUnit_FakeClock_AdvanceFiresTicker(t *testing.T) {
	t.Parallel()

	c := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ticker := c.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	select {
	case <-ticker.C():
		t.Fatal("ticker fired before Advance")
	default:
	}

	c.Advance(100 * time.Millisecond)

	select {
	case <-ticker.C():
	case <-time.After(time.Second):
		t.Fatal("ticker did not fire after Advance(100ms)")
	}
}

func TestUnit_FakeClock_AdvanceMultipleIntervals(t *testing.T) {
	t.Parallel()

	c := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ticker := c.NewTicker(time.Second)
	defer ticker.Stop()

	// Buffered chan of size 1 — like time.Ticker, drops extras silently.
	c.Advance(5 * time.Second)

	select {
	case <-ticker.C():
	case <-time.After(time.Second):
		t.Fatal("ticker did not fire after 5s Advance")
	}
}

func TestUnit_FakeClock_NowAdvances(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewFakeClock(start)

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now = %v, want %v", got, start)
	}

	c.Advance(42 * time.Second)
	want := start.Add(42 * time.Second)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("Now after Advance = %v, want %v", got, want)
	}
}

func TestUnit_FakeClock_StoppedTickerDoesNotFire(t *testing.T) {
	t.Parallel()

	c := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ticker := c.NewTicker(time.Second)
	ticker.Stop()

	c.Advance(10 * time.Second)

	select {
	case got := <-ticker.C():
		t.Fatalf("stopped ticker fired: %v", got)
	default:
	}
}

func TestUnit_RealClock_NowMonotonic(t *testing.T) {
	t.Parallel()

	c := clock.NewRealClock()
	a := c.Now()
	b := c.Now()
	if b.Before(a) {
		t.Fatalf("RealClock.Now non-monotonic: a=%v, b=%v", a, b)
	}
}

func TestUnit_RealClock_TickerFires(t *testing.T) {
	t.Parallel()

	c := clock.NewRealClock()
	ticker := c.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	select {
	case <-ticker.C():
	case <-time.After(time.Second):
		t.Fatal("RealClock ticker did not fire within 1s")
	}
}

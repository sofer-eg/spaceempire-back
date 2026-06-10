package clock

import (
	"sync"
	"time"
)

type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

func NewFakeClock(now time.Time) *FakeClock {
	return &FakeClock{now: now}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive ticker interval")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	t := &fakeTicker{
		c:        make(chan time.Time, 1),
		interval: d,
		next:     c.now.Add(d),
	}
	c.tickers = append(c.tickers, t)
	return t
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	snapshot := append([]*fakeTicker(nil), c.tickers...)
	c.mu.Unlock()

	for _, t := range snapshot {
		t.fireUpTo(now)
	}
}

type fakeTicker struct {
	c        chan time.Time
	interval time.Duration

	mu      sync.Mutex
	next    time.Time
	stopped bool
}

func (t *fakeTicker) C() <-chan time.Time {
	return t.c
}

func (t *fakeTicker) Stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
}

func (t *fakeTicker) fireUpTo(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for !t.stopped && !t.next.After(now) {
		select {
		case t.c <- t.next:
		default:
		}
		t.next = t.next.Add(t.interval)
	}
}

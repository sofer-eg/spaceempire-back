package racestanding

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/pkg/clock"
)

// CloserService is the slice of *Service the closer needs.
type CloserService interface {
	Decay(ctx context.Context) error
}

// Closer is the background goroutine that periodically decays every player's
// standing toward neutral (phase 9.4) — the slow reputation recovery. Mirrors
// economy/rent.Closer; cadence ~1h (standing is slow-moving).
type Closer struct {
	svc      CloserService
	clock    clock.Clock
	logger   *slog.Logger
	interval time.Duration
}

// NewCloser wires a Closer. A non-positive interval defaults to 1h; a nil
// logger to slog.Default.
func NewCloser(svc CloserService, clk clock.Clock, logger *slog.Logger, interval time.Duration) *Closer {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &Closer{svc: svc, clock: clk, logger: logger, interval: interval}
}

// Run blocks until ctx is canceled, decaying once per interval.
func (c *Closer) Run(ctx context.Context) {
	ticker := c.clock.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			c.Tick(ctx)
		}
	}
}

// Tick runs one decay pass. Exposed so tests can drive the closer with a
// controlled clock.
func (c *Closer) Tick(ctx context.Context) {
	if err := c.svc.Decay(ctx); err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Error("racestanding.closer.decay", "err", err)
	}
}

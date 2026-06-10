package rent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/pkg/clock"
)

// CloserBatch caps how many due rents one tick processes.
const CloserBatch = 200

// CloserService is the slice of *Service the closer needs.
type CloserService interface {
	Reconcile(ctx context.Context) error
	ProcessDue(ctx context.Context, limit int) error
}

// Closer is the background goroutine that periodically reconciles ownership
// into rent rows and charges due rents. Mirrors economy/auction.Closer; the
// design pegs the cadence to ~1h (rent is slow-moving).
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

// Run blocks until ctx is canceled, ticking once per interval.
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

// Tick reconciles ownership then charges due rents. A reconcile error is
// logged but does not skip the billing pass. Exposed so tests can drive the
// closer with a controlled clock.
func (c *Closer) Tick(ctx context.Context) {
	if err := c.svc.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Error("rent.closer.reconcile", "err", err)
	}
	if err := c.svc.ProcessDue(ctx, CloserBatch); err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Error("rent.closer.process_due", "err", err)
	}
}

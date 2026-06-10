package bounties

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/pkg/clock"
)

// CloserBatch caps how many due bounties one tick expires, keeping the closer
// from monopolizing the database during a backlog.
const CloserBatch = 100

// CloserService is the slice of *Service the closer needs. An interface so
// tests can stub it.
type CloserService interface {
	ExpireDue(ctx context.Context, limit int) (int, error)
}

// Closer is a background goroutine that polls for expired bounties and
// refunds+closes them. Mirrors economy/auction.Closer.
type Closer struct {
	svc      CloserService
	clock    clock.Clock
	logger   *slog.Logger
	interval time.Duration
}

// NewCloser wires a Closer. A non-positive interval defaults to 1s; a nil
// logger to slog.Default.
func NewCloser(svc CloserService, clk clock.Clock, logger *slog.Logger, interval time.Duration) *Closer {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = time.Second
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

// Tick processes one batch. Exposed so tests that control time can drive the
// closer directly.
func (c *Closer) Tick(ctx context.Context) {
	if _, err := c.svc.ExpireDue(ctx, CloserBatch); err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Error("bounties.closer.expire_due", "err", err)
	}
}

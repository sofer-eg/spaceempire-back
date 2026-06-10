package quest

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/pkg/clock"
)

// CloserBatch caps active quests processed per tick.
const CloserBatch = 500

// CloserService is the slice of *Service the closer needs.
type CloserService interface {
	ProcessAll(ctx context.Context, limit int) error
}

// Closer is the background goroutine that polls active quests and advances
// steps. Quests should feel responsive, so the cadence is a few seconds (not
// the hourly rent/bounty pace). Mirrors economy/rent.Closer.
type Closer struct {
	svc      CloserService
	clock    clock.Clock
	logger   *slog.Logger
	interval time.Duration
}

// NewCloser wires a Closer. A non-positive interval defaults to 5s; a nil
// logger to slog.Default.
func NewCloser(svc CloserService, clk clock.Clock, logger *slog.Logger, interval time.Duration) *Closer {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 5 * time.Second
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

// Tick processes one batch. Exposed so tests can drive it with a controlled
// clock.
func (c *Closer) Tick(ctx context.Context) {
	if err := c.svc.ProcessAll(ctx, CloserBatch); err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Error("quest.closer.process_all", "err", err)
	}
}

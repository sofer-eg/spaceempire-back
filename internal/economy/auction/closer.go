package auction

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/pkg/clock"
)

// CloserBatch caps how many due lots one tick closes. Keeps the closer
// from monopolizing the database during a backlog.
const CloserBatch = 100

// CloserService is the slice of *Service the closer needs. Declared as an
// interface so tests can stub it without spinning up a real Service.
type CloserService interface {
	DueLots(ctx context.Context, limit int) ([]int64, error)
	Close(ctx context.Context, lotID int64) error
}

// Closer is a background goroutine that polls for expired lots and closes
// them. It owns its own ticker driven by the same Clock as the service.
type Closer struct {
	svc      CloserService
	clock    clock.Clock
	logger   *slog.Logger
	interval time.Duration
}

// NewCloser wires a Closer. interval is the polling cadence; the design
// pegs it to 1 second.
func NewCloser(svc CloserService, clk clock.Clock, logger *slog.Logger, interval time.Duration) *Closer {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = time.Second
	}
	return &Closer{svc: svc, clock: clk, logger: logger, interval: interval}
}

// Run blocks until ctx is canceled, ticking once per interval and closing
// every due lot. Errors are logged and skipped — a single bad lot must not
// stop the loop.
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

// Tick processes one batch. Exposed so tests that already control time
// can drive the closer directly without racing against a real ticker
// being registered with FakeClock.
func (c *Closer) Tick(ctx context.Context) {
	ids, err := c.svc.DueLots(ctx, CloserBatch)
	if err != nil {
		c.logger.Error("auction.closer.due_lots", "err", err)
		return
	}
	for _, id := range ids {
		if err := c.svc.Close(ctx, id); err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Error("auction.closer.close", "lot", id, "err", err)
		}
	}
}

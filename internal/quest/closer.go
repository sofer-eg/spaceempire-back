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

// OfferSweeper purges TTL-expired offers on each closer tick (TASK-89, FR-06).
// Satisfied by *quests.OfferRepository.
type OfferSweeper interface {
	DeleteExpiredOffers(ctx context.Context, now time.Time) (int64, error)
}

// Closer is the background goroutine that polls active quests and advances
// steps, and sweeps TTL-expired procedural offers. Quests should feel
// responsive, so the cadence is a few seconds (not the hourly rent/bounty
// pace). Mirrors economy/rent.Closer.
type Closer struct {
	svc      CloserService
	offers   OfferSweeper
	clock    clock.Clock
	logger   *slog.Logger
	interval time.Duration
}

// NewCloser wires a Closer. offers may be nil (offer sweep disabled). A
// non-positive interval defaults to 5s; a nil logger to slog.Default.
func NewCloser(svc CloserService, offers OfferSweeper, clk clock.Clock, logger *slog.Logger, interval time.Duration) *Closer {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Closer{svc: svc, offers: offers, clock: clk, logger: logger, interval: interval}
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

// Tick processes one batch and sweeps expired offers. Exposed so tests can
// drive it with a controlled clock. The offer sweep does not race a concurrent
// accept: accept deletes its offer inside its own transaction, so the sweep can
// only remove offers no accept still holds.
func (c *Closer) Tick(ctx context.Context) {
	if err := c.svc.ProcessAll(ctx, CloserBatch); err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Error("quest.closer.process_all", "err", err)
	}
	if c.offers == nil {
		return
	}
	n, err := c.offers.DeleteExpiredOffers(ctx, c.clock.Now())
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			c.logger.Error("quest.closer.delete_expired_offers", "err", err)
		}
		return
	}
	if n > 0 {
		c.logger.Info("quest.closer.offers_expired", "count", n)
	}
}

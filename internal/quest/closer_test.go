package quest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/quest"
)

type fakeCloserSvc struct{ processed int }

func (f *fakeCloserSvc) ProcessAll(context.Context, int) error { f.processed++; return nil }

type fakeSweeper struct {
	calls   int
	lastNow time.Time
	ret     int64
}

func (f *fakeSweeper) DeleteExpiredOffers(_ context.Context, now time.Time) (int64, error) {
	f.calls++
	f.lastNow = now
	return f.ret, nil
}

// TestUnit_QuestCloser_TickSweepsOffers — each tick runs ProcessAll and sweeps
// expired offers at the clock's now (FR-06 TTL).
func TestUnit_QuestCloser_TickSweepsOffers(t *testing.T) {
	t.Parallel()
	svc := &fakeCloserSvc{}
	sweeper := &fakeSweeper{ret: 2}
	clk := clock.NewFakeClock(epoch)
	closer := quest.NewCloser(svc, sweeper, clk, nil, time.Second)

	closer.Tick(context.Background())

	assert.Equal(t, 1, svc.processed, "poller ran")
	require.Equal(t, 1, sweeper.calls, "offers swept")
	assert.Equal(t, clk.Now(), sweeper.lastNow, "swept at clock now")
}

// TestUnit_QuestCloser_NilSweeper — a nil sweeper disables the offer sweep
// without panicking (still runs the poller).
func TestUnit_QuestCloser_NilSweeper(t *testing.T) {
	t.Parallel()
	svc := &fakeCloserSvc{}
	closer := quest.NewCloser(svc, nil, clock.NewFakeClock(epoch), nil, time.Second)
	closer.Tick(context.Background())
	assert.Equal(t, 1, svc.processed)
}

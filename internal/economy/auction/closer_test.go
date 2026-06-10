package auction_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/economy/auction"
	"spaceempire/back/internal/pkg/clock"
)

// fakeCloserSvc records what the closer asks of the service so the test
// can assert ordering and idempotency without exercising the full Service.
type fakeCloserSvc struct {
	mu sync.Mutex

	due       []int64
	closed    []int64
	dueErr    error
	closedErr map[int64]error
}

func (f *fakeCloserSvc) DueLots(_ context.Context, _ int) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	out := append([]int64(nil), f.due...)
	f.due = nil
	return out, nil
}

func (f *fakeCloserSvc) Close(_ context.Context, lotID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, lotID)
	return f.closedErr[lotID]
}

func TestUnit_Closer_ClosesEveryDueLotPerTick(t *testing.T) {
	svc := &fakeCloserSvc{due: []int64{10, 20, 30}}
	clk := clock.NewFakeClock(time.Now())
	closer := auction.NewCloser(svc, clk, nil, time.Second)

	closer.Tick(context.Background())

	svc.mu.Lock()
	defer svc.mu.Unlock()
	assert.Equal(t, []int64{10, 20, 30}, svc.closed)
}

func TestUnit_Closer_SurvivesCloseError(t *testing.T) {
	svc := &fakeCloserSvc{
		due:       []int64{10, 20},
		closedErr: map[int64]error{10: errors.New("transient")},
	}
	clk := clock.NewFakeClock(time.Now())
	closer := auction.NewCloser(svc, clk, nil, time.Second)

	closer.Tick(context.Background())

	svc.mu.Lock()
	defer svc.mu.Unlock()
	assert.Equal(t, []int64{10, 20}, svc.closed)
}

func TestUnit_Closer_SurvivesDueLotsError(t *testing.T) {
	svc := &fakeCloserSvc{dueErr: errors.New("db down")}
	clk := clock.NewFakeClock(time.Now())
	closer := auction.NewCloser(svc, clk, nil, time.Second)

	// Must not panic. No lots are closed because DueLots failed.
	closer.Tick(context.Background())

	svc.mu.Lock()
	defer svc.mu.Unlock()
	assert.Empty(t, svc.closed)
}

func TestUnit_Closer_StopsOnContextCancel(t *testing.T) {
	svc := &fakeCloserSvc{}
	clk := clock.NewFakeClock(time.Now())
	closer := auction.NewCloser(svc, clk, nil, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		closer.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closer did not exit on context cancel")
	}
}

package bus_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
)

func TestUnit_InMemory_PublishAfterSubscribe_DeliversToHandler(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan []byte, 1)
	require.NoError(t, b.Subscribe(ctx, "topic.A", func(p []byte) {
		received <- p
	}))

	require.NoError(t, b.Publish(ctx, "topic.A", []byte("hello")))

	select {
	case got := <-received:
		assert.Equal(t, []byte("hello"), got)
	case <-time.After(time.Second):
		t.Fatal("handler was not invoked")
	}
}

func TestUnit_InMemory_PublishBeforeSubscribe_IsDropped(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, b.Publish(ctx, "topic.A", []byte("lost")))

	got := make(chan []byte, 1)
	require.NoError(t, b.Subscribe(ctx, "topic.A", func(p []byte) {
		got <- p
	}))

	select {
	case <-got:
		t.Fatal("subscriber should not see messages published before it joined")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnit_InMemory_FanoutToMultipleSubscribers(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu   sync.Mutex
		seen []string
	)
	register := func(name string) {
		require.NoError(t, b.Subscribe(ctx, "topic.fanout", func(p []byte) {
			mu.Lock()
			seen = append(seen, name+":"+string(p))
			mu.Unlock()
		}))
	}
	register("a")
	register("b")

	require.NoError(t, b.Publish(ctx, "topic.fanout", []byte("msg")))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	}, time.Second, 5*time.Millisecond)
}

func TestUnit_InMemory_PerSubscriberPayloadIsIndependent(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan []byte, 1)
	require.NoError(t, b.Subscribe(ctx, "topic.X", func(p []byte) {
		got <- p
	}))

	payload := []byte("immutable")
	require.NoError(t, b.Publish(ctx, "topic.X", payload))

	received := <-got
	received[0] = 'X' // must not corrupt the publisher's slice
	assert.Equal(t, byte('i'), payload[0])
}

func TestUnit_InMemory_UnsubscribeOnCtxCancel(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(8)
	parent := context.Background()
	subCtx, cancelSub := context.WithCancel(parent)

	got := make(chan struct{}, 1)
	require.NoError(t, b.Subscribe(subCtx, "topic.Y", func(_ []byte) {
		got <- struct{}{}
	}))

	cancelSub()
	// Give the subscriber goroutine a moment to deregister.
	require.Eventually(t, func() bool {
		err := b.Publish(parent, "topic.Y", []byte("after-cancel"))
		require.NoError(t, err)
		select {
		case <-got:
			return false
		case <-time.After(20 * time.Millisecond):
			return true
		}
	}, time.Second, 50*time.Millisecond)
}

func TestUnit_InMemory_PublishAfterClose_ReturnsErrClosed(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(8)
	b.Close()

	err := b.Publish(context.Background(), "topic.Z", []byte("nope"))
	assert.ErrorIs(t, err, bus.ErrClosed)
}

func TestUnit_InMemory_SubscribeAfterClose_ReturnsErrClosed(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(8)
	b.Close()

	err := b.Subscribe(context.Background(), "topic.Z", func(_ []byte) {})
	assert.ErrorIs(t, err, bus.ErrClosed)
}

func TestUnit_InMemory_PreservesOrderPerSubscriber(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const N = 50
	got := make(chan int, N)
	require.NoError(t, b.Subscribe(ctx, "topic.order", func(p []byte) {
		var n int
		_, _ = fmt.Sscanf(string(p), "%d", &n)
		got <- n
	}))

	for i := 0; i < N; i++ {
		require.NoError(t, b.Publish(ctx, "topic.order", []byte(fmt.Sprintf("%d", i))))
	}

	for i := 0; i < N; i++ {
		select {
		case v := <-got:
			assert.Equal(t, i, v)
		case <-time.After(time.Second):
			t.Fatalf("missing message %d", i)
		}
	}
}

func TestUnit_InMemory_ConcurrentPublishersAndSubscribers(t *testing.T) {
	t.Parallel()

	b := bus.NewInMemory(256)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		subs  = 4
		pubs  = 4
		each  = 100
		total = subs * pubs * each
	)

	var count atomic.Int64
	for s := 0; s < subs; s++ {
		require.NoError(t, b.Subscribe(ctx, "topic.concurrent", func(_ []byte) {
			count.Add(1)
		}))
	}

	var wg sync.WaitGroup
	for p := 0; p < pubs; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_ = b.Publish(ctx, "topic.concurrent", []byte("x"))
			}
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		return count.Load() == int64(total)
	}, 2*time.Second, 10*time.Millisecond)
}

package bus

import (
	"context"
	"sync"
)

// InMemory is a process-local Bus. Each topic owns a goroutine per
// subscriber: Publish delivers to that goroutine through a buffered
// channel, the goroutine in turn calls the user handler. This keeps
// Publish non-blocking under normal load and isolates a slow handler in
// one topic from publishers on another.
//
// Buffer is fixed-size; if a subscriber falls behind by more than
// SubscriberBuffer messages Publish blocks until the goroutine catches
// up (or ctx is canceled). For handoff specifically this is the right
// trade-off — we want back-pressure, not silent loss.
type InMemory struct {
	mu          sync.RWMutex
	closed      bool
	subscribers map[string][]*subscription
	bufSize     int
}

type subscription struct {
	ch chan []byte
}

// NewInMemory builds an in-process Bus. subscriberBuffer is the per-
// subscriber channel capacity (>= 1; falls back to 64 when not positive).
func NewInMemory(subscriberBuffer int) *InMemory {
	if subscriberBuffer <= 0 {
		subscriberBuffer = 64
	}
	return &InMemory{
		subscribers: make(map[string][]*subscription),
		bufSize:     subscriberBuffer,
	}
}

// Publish copies the payload into every subscriber channel for topic.
// Returns ErrClosed if the Bus has been Close()d; ctx errors if a
// subscriber is full and ctx expires before the send goes through.
func (b *InMemory) Publish(ctx context.Context, topic string, payload []byte) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return ErrClosed
	}
	subs := b.subscribers[topic]
	// Take a snapshot of the slice header so we can release the lock
	// before talking to subscriber channels — handler-time back-pressure
	// must not stall future Subscribe calls.
	snap := make([]*subscription, len(subs))
	copy(snap, subs)
	b.mu.RUnlock()

	for _, sub := range snap {
		// Defensive copy: subscribers must not observe each other's
		// mutations if a downstream handler ever decides to retain the
		// slice. Cheap for the small payloads we send (handoff events).
		cp := make([]byte, len(payload))
		copy(cp, payload)
		select {
		case sub.ch <- cp:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Subscribe registers handler for topic and returns once the subscription
// is live. handler is invoked from a dedicated goroutine in payload-order;
// when ctx is canceled the goroutine drains pending messages and exits,
// and the subscription is removed from the topic.
func (b *InMemory) Subscribe(ctx context.Context, topic string, handler func([]byte)) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	sub := &subscription{ch: make(chan []byte, b.bufSize)}
	b.subscribers[topic] = append(b.subscribers[topic], sub)
	b.mu.Unlock()

	go b.run(ctx, topic, sub, handler)
	return nil
}

// Close marks the Bus closed for new operations. Existing subscription
// goroutines exit on their own ctx; we do not close their channels here
// (publishers may still be writing) — they simply stop receiving once
// Publish refuses with ErrClosed.
func (b *InMemory) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

func (b *InMemory) run(ctx context.Context, topic string, sub *subscription, handler func([]byte)) {
	defer b.removeSubscriber(topic, sub)
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-sub.ch:
			handler(msg)
		}
	}
}

func (b *InMemory) removeSubscriber(topic string, target *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subscribers[topic]
	for i, s := range subs {
		if s == target {
			b.subscribers[topic] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(b.subscribers[topic]) == 0 {
		delete(b.subscribers, topic)
	}
}

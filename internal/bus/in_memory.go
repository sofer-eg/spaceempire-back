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
// Returns ErrClosed if the Bus has been Close()d, and a *PartialDelivery
// (wrapping the context error) when ctx expires before some subscriber could
// take it.
//
// Delivery semantics under a deadline — this is what callers must reason about
// before putting a timeout on a Publish (TASK-148 review):
//
//   - EVERY subscriber is attempted. A ctx that expires on one subscriber does
//     not skip the ones behind it in the list, which is what the first version
//     of this loop did: it returned at the first blocked send, so a single slow
//     handler silently starved every handler registered after it. On
//     EntityKilledTopic that meant a slow bounty payout could cost a dead player
//     his spacesuit.
//   - A subscriber with room in its buffer ALWAYS receives the payload, even
//     when ctx is already expired. That is why the send is tried non-blocking
//     first: a plain `select` over both cases picks randomly when both are
//     ready, so an expired context would rob ready subscribers half the time.
//   - Only a subscriber whose buffer is full for the remainder of ctx misses
//     the payload, and it is named (by index within the topic) in the returned
//     PartialDelivery so the caller can log who.
//
// None of that makes a deadline safe for a topic whose delivery IS a state
// change — a missed subscriber is a lost effect, and this bus has no retry. Such
// topics must be published without a deadline (back-pressure), which is what
// sector.Worker.publishEffect does.
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

	var undelivered []int
	for i, sub := range snap {
		// Defensive copy: subscribers must not observe each other's
		// mutations if a downstream handler ever decides to retain the
		// slice. Cheap for the small payloads we send (handoff events).
		cp := make([]byte, len(payload))
		copy(cp, payload)
		select {
		case sub.ch <- cp:
			continue
		default:
		}
		select {
		case sub.ch <- cp:
		case <-ctx.Done():
			undelivered = append(undelivered, i)
		}
	}
	if len(undelivered) > 0 {
		return &PartialDelivery{
			Topic:       topic,
			Subscribers: len(snap),
			Undelivered: undelivered,
			Cause:       ctx.Err(),
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

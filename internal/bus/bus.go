// Package bus is the thin abstraction over inter-component message passing
// used by sector handoff and (later) other cross-sector / cross-process
// events. The interface is intentionally [][]byte-flat so the in-memory
// implementation here can be swapped for Redis Streams / NATS without
// touching call sites.
package bus

import (
	"context"
	"errors"
	"fmt"
)

// ErrClosed is reported when Publish/Subscribe is called on a Bus that has
// been Close()d.
var ErrClosed = errors.New("bus: closed")

// PartialDelivery reports that a bounded Publish reached some subscribers of a
// topic but not all of them: the ones listed in Undelivered still had a full
// buffer when the context expired. It wraps the context error, so
// errors.Is(err, context.DeadlineExceeded) keeps working for callers that only
// care whether the deadline fired.
//
// It exists because "publish failed" is not a useful thing to log on a
// multi-subscriber topic — the caller needs to know that the other subscribers
// DID get it, and which one did not, since nothing will retry.
type PartialDelivery struct {
	Topic       string
	Subscribers int
	// Undelivered holds the indices (registration order within the topic) of
	// the subscribers that did not receive the payload.
	Undelivered []int
	Cause       error
}

func (e *PartialDelivery) Error() string {
	return fmt.Sprintf("bus: topic %q delivered to %d of %d subscribers (missed %v): %v",
		e.Topic, e.Subscribers-len(e.Undelivered), e.Subscribers, e.Undelivered, e.Cause)
}

func (e *PartialDelivery) Unwrap() error { return e.Cause }

// Publisher emits payloads to a topic. Topic naming convention used by
// sector handoff: "sector.<N>.intake".
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// Subscriber registers a handler for a topic. handler is invoked from the
// Subscriber's own goroutine — it must not block; if real work is needed,
// enqueue and return. Subscription lives until ctx is canceled.
type Subscriber interface {
	Subscribe(ctx context.Context, topic string, handler func([]byte)) error
}

// Bus combines Publisher and Subscriber. The in-memory implementation
// satisfies both; a Redis adapter would too.
type Bus interface {
	Publisher
	Subscriber
}

// Package bus is the thin abstraction over inter-component message passing
// used by sector handoff and (later) other cross-sector / cross-process
// events. The interface is intentionally [][]byte-flat so the in-memory
// implementation here can be swapped for Redis Streams / NATS without
// touching call sites.
package bus

import (
	"context"
	"errors"
)

// ErrClosed is reported when Publish/Subscribe is called on a Bus that has
// been Close()d.
var ErrClosed = errors.New("bus: closed")

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

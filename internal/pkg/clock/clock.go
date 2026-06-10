package clock

import "time"

type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

package clock

import "time"

type RealClock struct{}

func NewRealClock() RealClock {
	return RealClock{}
}

func (RealClock) Now() time.Time {
	return time.Now()
}

func (RealClock) NewTicker(d time.Duration) Ticker {
	return realTicker{t: time.NewTicker(d)}
}

type realTicker struct {
	t *time.Ticker
}

func (r realTicker) C() <-chan time.Time {
	return r.t.C
}

func (r realTicker) Stop() {
	r.t.Stop()
}

package sector

import "time"

// WithDBDurationSource replaces the measurement the drain's DB budget is charged
// against (Worker.dbSince, time.Since in production). Test-only: this file is
// compiled into the package's test binary alone, so it adds nothing to the
// worker's public API.
//
// It exists because the budget is deliberately charged in real time — the
// injected clock does not model time parked on DB I/O — which left the budget
// tests asserting on how long a fake call took under scheduler load, i.e.
// flaky under parallel load (TASK-154). A test states the cost of a modelled DB
// call instead: RepoTimeout for one that ran to its deadline, zero for a healthy
// one. Everything the budget then does — the per-drain reset, the subtraction,
// the stop check — runs as it does in production.
func WithDBDurationSource(since func(started time.Time) time.Duration) Option {
	return func(w *Worker) {
		w.dbSince = since
	}
}

// DBCallCost exposes the measurement itself, which is what every other budget
// test replaces and therefore what none of them covers: with
// WithDBDurationSource in play, a production measurement that returned zero (or
// read the injected clock) would leave the whole unit suite green while a hung
// Postgres stopped charging the drain budget — the TASK-144 regress it exists to
// prevent. TestUnit_Worker_DBBudgetChargesRealElapsedTime is the one test that
// holds it.
func (w *Worker) DBCallCost(started time.Time) time.Duration {
	return w.dbCallCost(started)
}

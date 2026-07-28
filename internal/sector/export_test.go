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

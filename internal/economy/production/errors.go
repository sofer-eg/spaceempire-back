// Package production runs the station factory cycle that the legacy
// MySQL procedure TO_Production used to drive. See
// docs/specs/production.md for the full contract.
package production

import "errors"

// ErrNoBalance is returned by New when the caller passes a nil *Balance.
var ErrNoBalance = errors.New("production: balance is required")

// ErrNoTxRunner is returned by New when the caller passes a nil TxRunner.
var ErrNoTxRunner = errors.New("production: tx runner is required")

package insurance

import "errors"

var (
	// ErrInvalidInput covers a non-positive premium or duration. → 400.
	ErrInvalidInput = errors.New("insurance: invalid input")
	// ErrNotOwner is returned when the caller does not own the ship. → 403.
	ErrNotOwner = errors.New("insurance: not your ship")
	// ErrNotDocked is returned when the ship is not docked (insurance is
	// bought at a station). → 409.
	ErrNotDocked = errors.New("insurance: ship must be docked")
	// ErrShipNotFound is returned when the ship does not exist. → 404.
	ErrShipNotFound = errors.New("insurance: ship not found")
	// ErrAlreadyInsured is returned when the ship already has an active
	// policy. → 409.
	ErrAlreadyInsured = errors.New("insurance: ship already insured")
	// ErrInsufficientFunds is returned when the player cannot pay the
	// premium. → 409.
	ErrInsufficientFunds = errors.New("insurance: insufficient funds")
)

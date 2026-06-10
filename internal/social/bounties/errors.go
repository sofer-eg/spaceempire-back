package bounties

import "errors"

var (
	// ErrInvalidInput covers a non-positive amount or an unsupported
	// target/sponsor kind. Surfaces as 400.
	ErrInvalidInput = errors.New("bounties: invalid input")
	// ErrSelfBounty is returned when a caller tries to place a bounty on
	// themselves (or their own clan when funding from it). Surfaces as 400.
	ErrSelfBounty = errors.New("bounties: cannot target yourself")
	// ErrInsufficientFunds is returned when the sponsor wallet (player cash
	// or clan treasury) cannot cover the amount. Surfaces as 409.
	ErrInsufficientFunds = errors.New("bounties: insufficient funds")
	// ErrNotInClan is returned when a player funds a clan bounty but belongs
	// to no clan. Surfaces as 409.
	ErrNotInClan = errors.New("bounties: not in a clan")
	// ErrNotClanLeader is returned when a non-leader funds a clan bounty
	// (6.1 only mints leader/member). Surfaces as 403.
	ErrNotClanLeader = errors.New("bounties: only the clan leader can fund a clan bounty")
)

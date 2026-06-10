// Package auction implements the global lot-bid-close cycle. A Service
// composes persistence/auction with persistence/players (cash escrow) and
// persistence/cargo (seller debit, winner deposit) inside one transaction
// per public call, so concurrent bids on the same lot can produce exactly
// one winner.
package auction

import "errors"

// ErrLotNotFound is returned when the requested lot id does not exist.
var ErrLotNotFound = errors.New("auction: lot not found")

// ErrLotNotActive is returned by Bid when the lot is closed or cancelled,
// or when its timer has already expired. Maps to HTTP 410 Gone.
var ErrLotNotActive = errors.New("auction: lot is not active")

// ErrBidTooLow is returned by Bid when amount <= current_price.
var ErrBidTooLow = errors.New("auction: bid below current price")

// ErrInsufficientCash is returned by Bid when the bidder cannot afford
// the bid. Maps to HTTP 402.
var ErrInsufficientCash = errors.New("auction: insufficient cash")

// ErrSellerBid is returned by Bid when the bidder is the lot's seller.
var ErrSellerBid = errors.New("auction: seller cannot bid on own lot")

// ErrInvalidQuantity is returned by Create when quantity is <= 0.
var ErrInvalidQuantity = errors.New("auction: quantity must be positive")

// ErrInvalidStartPrice is returned by Create when start price is <= 0.
var ErrInvalidStartPrice = errors.New("auction: start price must be positive")

// ErrInvalidDuration is returned by Create when duration is below the
// floor or above the ceiling defined by the service.
var ErrInvalidDuration = errors.New("auction: duration out of range")

// ErrInsufficientCargo is returned by Create when the seller's source
// owner does not carry enough of the requested goods.
var ErrInsufficientCargo = errors.New("auction: source does not carry enough goods")

// ErrNotDocked is returned by Create/Bid when the player's ship is not
// docked at a station. Like trade, all market interaction (the X-Tension
// model) requires a dock. Maps to HTTP 409 Conflict.
var ErrNotDocked = errors.New("auction: ship is not docked")

// ErrForbidden is returned by Create/Bid when the ship is not owned by the
// acting player. Maps to HTTP 403.
var ErrForbidden = errors.New("auction: ship not owned by player")

// ErrShipNotFound is returned by Create/Bid when the referenced ship row
// does not exist. Maps to HTTP 404.
var ErrShipNotFound = errors.New("auction: ship not found")

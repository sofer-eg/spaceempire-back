package domain

import "time"

type BountyID int64

// BountyStatus is the lifecycle of a bounty: active until either claimed
// (paid) or timed out (expired). Stored as the TEXT status column.
type BountyStatus string

const (
	BountyActive  BountyStatus = "active"
	BountyPaid    BountyStatus = "paid"
	BountyExpired BountyStatus = "expired"
)

// Bounty is a funded contract on an entity's head (phase 6.3). Target and
// Sponsor are EntityRefs of kind player or clan. The sponsor's funds are
// escrowed at set time: credited to the killer on a claim, refunded to the
// sponsor on expiry. See back/docs/specs/bounties.md.
type Bounty struct {
	ID        BountyID
	Target    EntityRef
	Sponsor   EntityRef
	Amount    int64
	Status    BountyStatus
	CreatedAt time.Time
	ExpiresAt time.Time
	// PaidTo is the player credited on a payout; 0 until Status == paid.
	PaidTo PlayerID
	// PaidAt is the payout timestamp; zero until Status == paid.
	PaidAt time.Time
}

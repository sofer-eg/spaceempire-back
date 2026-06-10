package domain

import "time"

type PolicyID int64

// PolicyStatus is the lifecycle of an insurance policy: active until it either
// pays out (claimed) or lapses (expired).
type PolicyStatus string

const (
	PolicyActive  PolicyStatus = "active"
	PolicyClaimed PolicyStatus = "claimed"
	PolicyExpired PolicyStatus = "expired"
)

// InsurancePolicy covers one ship against destruction (phase 6.5). The player
// pays PremiumPaid up front; if the ship is destroyed while the policy is
// active and unexpired, the holder is paid Coverage and the policy is
// claimed. Reinterpretation of the old insure.php — see
// back/docs/specs/insurance.md.
type InsurancePolicy struct {
	ID          PolicyID
	ShipID      ShipID
	PlayerID    PlayerID
	PremiumPaid int64
	Coverage    int64
	Status      PolicyStatus
	CreatedAt   time.Time
	ExpiresAt   time.Time
	// ClaimedAt is the payout timestamp; zero until Status == claimed.
	ClaimedAt time.Time
}

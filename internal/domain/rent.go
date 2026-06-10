package domain

import "time"

type RentID int64

// Rent is a periodic upkeep obligation a player owes on a station-like object
// they own (phase 6.4). Each billing period the payer's cash is debited by
// AmountPerPeriod; non-payment increments UnpaidPeriods, and after the
// configured limit the object is confiscated (owner cleared to NPC/gov).
// Station is an EntityRef of kind station/shipyard/trade_station. Port of the
// idea behind SP TO_RentCheck (the original charges cargo-storage rent; 6.4
// reinterprets it as ownership upkeep). See back/docs/specs/rent.md.
type Rent struct {
	ID              RentID
	Payer           PlayerID
	Station         EntityRef
	AmountPerPeriod int64
	// UnpaidPeriods counts consecutive missed payments; reset to 0 on a
	// successful charge.
	UnpaidPeriods int
	// LastPaidAt is the last successful charge; zero until the first payment.
	LastPaidAt time.Time
	// NextDueAt is when the next charge is owed.
	NextDueAt time.Time
	CreatedAt time.Time
}

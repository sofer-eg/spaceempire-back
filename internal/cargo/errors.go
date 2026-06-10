// Package cargo orchestrates inventory operations across owners (ships,
// stations, trade stations) on top of the persistence/cargo repository.
// All multi-step writes go through the injected TxRunner so capacity
// checks and stack mutations are atomic.
package cargo

import "errors"

// ErrSameOwner is returned by Move when the from and to entities are
// identical — moving cargo to itself is a no-op the caller likely
// got wrong.
var ErrSameOwner = errors.New("cargo: source and destination are the same")

// ErrNonPositiveQuantity is returned when qty is zero or negative.
var ErrNonPositiveQuantity = errors.New("cargo: quantity must be positive")

// ErrNoSpace is returned by Move when the destination cargobay cannot
// fit the requested quantity.
var ErrNoSpace = errors.New("cargo: destination has insufficient space")

// ErrGoodsTypeNotFound is returned when the requested goods_type does
// not exist in the reference table.
var ErrGoodsTypeNotFound = errors.New("cargo: goods type not found")

// ErrInsufficientQuantity is returned by Move when the source does not
// hold enough of the requested goods type.
var ErrInsufficientQuantity = errors.New("cargo: insufficient quantity")

// ErrOwnerNotFound is returned when an owner referenced in the call
// has no row in its backing table (ships / stations / trade_stations).
var ErrOwnerNotFound = errors.New("cargo: owner not found")

// ErrUnsupportedOwnerKind is returned when an EntityKind has no cargobay
// in this phase (Pirbase, Shipyard, Container).
var ErrUnsupportedOwnerKind = errors.New("cargo: unsupported owner kind")

// ErrForbidden is returned by Move when a player tries to withdraw goods from
// a station hold that belong to a different player. Phase 10.22 — only the
// depositor (or anyone, for the unowned pool) may pull a stack out. HTTP maps
// it to 403.
var ErrForbidden = errors.New("cargo: forbidden")

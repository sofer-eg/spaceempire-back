// Package trade exposes the buy/sell operations a docked player can run
// against a station, trade station or pirbase. Every Buy/Sell call runs
// inside a single transaction so cash, station stock and cargo move
// together — there is no intermediate state a concurrent read could see.
package trade

import "errors"

// ErrShipNotFound is returned when the requested ship does not exist.
var ErrShipNotFound = errors.New("trade: ship not found")

// ErrForbidden is returned when the authenticated player does not own
// the ship referenced in the request.
var ErrForbidden = errors.New("trade: ship belongs to another player")

// ErrNotDocked is returned when the ship is not docked at any station.
var ErrNotDocked = errors.New("trade: ship is not docked")

// ErrWrongStation is returned when the ship is docked, but not at the
// station referenced in the request.
var ErrWrongStation = errors.New("trade: ship docked at a different station")

// ErrInvalidStationKind is returned when the request targets an entity
// that is not a station, trade station, or pirbase.
var ErrInvalidStationKind = errors.New("trade: station kind not tradeable")

// ErrMarketEntryNotFound is returned when the station does not offer the
// requested goods type at all.
var ErrMarketEntryNotFound = errors.New("trade: station does not offer this good")

// ErrStationDoesNotSell is returned by Buy when the station offers the
// good only for purchase (sell_price IS NULL).
var ErrStationDoesNotSell = errors.New("trade: station does not sell this good")

// ErrStationDoesNotBuy is returned by Sell when the station only sells
// the good (buy_price IS NULL).
var ErrStationDoesNotBuy = errors.New("trade: station does not buy this good")

// ErrNonPositiveQuantity is returned when qty is zero or negative.
var ErrNonPositiveQuantity = errors.New("trade: quantity must be positive")

// ErrInsufficientStock is returned by Buy when the station's stock is
// below the requested quantity.
var ErrInsufficientStock = errors.New("trade: station does not have enough stock")

// ErrStockOverflow is returned by Sell when the station cannot accept
// the quantity (would exceed max_stock).
var ErrStockOverflow = errors.New("trade: station cannot accept this much")

// ErrInsufficientCash is returned by Buy when the player cannot afford
// the requested quantity at the station's sell price.
var ErrInsufficientCash = errors.New("trade: player cannot afford this trade")

// ErrInsufficientCargo is returned by Sell when the ship's hold does not
// contain enough of the requested good.
var ErrInsufficientCargo = errors.New("trade: ship does not carry enough of this good")

// ErrNoCargoSpace is returned by Buy when the ship's hold cannot fit the
// purchased quantity given its current usage.
var ErrNoCargoSpace = errors.New("trade: ship cargobay cannot fit this purchase")

// ErrGoodsTypeNotFound is returned when the goods_type referenced does
// not exist in the reference table.
var ErrGoodsTypeNotFound = errors.New("trade: goods type not found")

package balance

import "errors"

// ErrDuplicateGoodsID is returned when two goods entries share the same id.
var ErrDuplicateGoodsID = errors.New("balance: duplicate goods id")

// ErrInvalidGoodsID is returned when a goods entry has a non-positive id.
var ErrInvalidGoodsID = errors.New("balance: goods id must be positive")

// ErrEmptyGoodsName is returned when a goods entry has a blank name.
var ErrEmptyGoodsName = errors.New("balance: goods name must not be empty")

// ErrNegativeGoodsField is returned when avg_price, max_price or space is
// below zero. Zero is allowed (e.g. quest-only or special goods with no
// cargo footprint), only negatives are rejected.
var ErrNegativeGoodsField = errors.New("balance: numeric goods field must not be negative")

// ErrInvalidRecipeCycle is returned when a recipe declares cycle_time <= 0.
var ErrInvalidRecipeCycle = errors.New("balance: recipe cycle_time must be positive")

// ErrEmptyRecipeOutputs is returned when a recipe has no outputs.
var ErrEmptyRecipeOutputs = errors.New("balance: recipe must declare at least one output")

// ErrUnknownRecipeGoods is returned when a recipe references a goods id
// missing from goods_types.
var ErrUnknownRecipeGoods = errors.New("balance: recipe references unknown goods id")

// ErrInvalidRecipeQuantity is returned when a recipe line has qty <= 0.
var ErrInvalidRecipeQuantity = errors.New("balance: recipe line qty must be positive")

// ErrInvalidRecipeMax is returned when a recipe line declares a negative max.
var ErrInvalidRecipeMax = errors.New("balance: recipe line max must be >= 0")

// ErrDuplicateRecipe is returned when two recipes share the same station_type.
var ErrDuplicateRecipe = errors.New("balance: duplicate recipe station_type")

// ErrInvalidShipClassID is returned when a ship class has a non-positive id.
var ErrInvalidShipClassID = errors.New("balance: ship class id must be positive")

// ErrEmptyShipClassName is returned when a ship class has a blank name.
var ErrEmptyShipClassName = errors.New("balance: ship class name must not be empty")

// ErrDuplicateShipClassID is returned when two ship classes share an id.
var ErrDuplicateShipClassID = errors.New("balance: duplicate ship class id")

// ErrInvalidStationTypeID is returned when a station type has a negative id.
var ErrInvalidStationTypeID = errors.New("balance: station type id must be >= 0")

// ErrEmptyStationTypeName is returned when a station type has a blank name.
var ErrEmptyStationTypeName = errors.New("balance: station type name must not be empty")

// ErrDuplicateStationTypeID is returned when two station types share an id.
var ErrDuplicateStationTypeID = errors.New("balance: duplicate station type id")

// ErrInvalidEquipmentID is returned when an equipment row has a non-positive id.
var ErrInvalidEquipmentID = errors.New("balance: equipment id must be positive")

// ErrEmptyEquipmentType is returned when an equipment row has a blank type.
var ErrEmptyEquipmentType = errors.New("balance: equipment type must not be empty")

// ErrDuplicateEquipmentID is returned when two equipment rows share an id.
var ErrDuplicateEquipmentID = errors.New("balance: duplicate equipment id")

// --- equipment install validation (phase 10.14) ---------------------------

// ErrEquipmentNotFound is returned when an install references an equipment id
// absent from the catalog.
var ErrEquipmentNotFound = errors.New("balance: equipment not found")

// ErrEquipmentWrongClass is returned when the equipment row's ship class does
// not match the target ship's class (and the row is not universal, class 0).
var ErrEquipmentWrongClass = errors.New("balance: equipment not available for this ship class")

// ErrEquipmentWrongRace is returned when a race-restricted equipment row does
// not match the ship's race.
var ErrEquipmentWrongRace = errors.New("balance: equipment not available for this race")

// ErrEquipmentLevel is returned when the requested install level is outside
// 1..max_level.
var ErrEquipmentLevel = errors.New("balance: invalid equipment level")

// ErrEquipmentDependency is returned when the module's prerequisite (its
// Dependance type) is not installed on the ship.
var ErrEquipmentDependency = errors.New("balance: equipment dependency not installed")

// ErrEquipmentAlreadyInstalled is returned when a module of the same type is
// already fitted (one module per type; uninstall first).
var ErrEquipmentAlreadyInstalled = errors.New("balance: equipment of this type already installed")

// ErrEquipmentNotInstalled is returned by uninstall validation when no module
// of the requested type is fitted.
var ErrEquipmentNotInstalled = errors.New("balance: equipment of this type not installed")

// ErrRankTooLow is returned when the player's reputation does not meet the
// module's min_war_rate / min_trade_rate / min_race_rate threshold (phase
// 10.3.4).
var ErrRankTooLow = errors.New("balance: player rank too low for this equipment")

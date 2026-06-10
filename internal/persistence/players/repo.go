// Package players persists per-player wallet state (cash). Auth-side
// account creation lives in internal/auth — this package is scoped to
// mutations that trade.Service (and later production/auction) need to
// run inside their own transactions.
package players

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrPlayerNotFound is returned when no row matches the player id.
var ErrPlayerNotFound = errors.New("players: not found")

// ErrInsufficientCash is returned by AdjustCash when a negative delta
// would drive the balance below zero.
var ErrInsufficientCash = errors.New("players: insufficient cash")

// Repository talks to the players.cash column via an Executor.
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor.
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const getCashSQL = `SELECT cash FROM players WHERE id = $1`

// GetCash returns the player's current balance.
func (r *Repository) GetCash(ctx context.Context, playerID domain.PlayerID) (int64, error) {
	var cash int64
	err := r.exec.QueryRow(ctx, getCashSQL, int64(playerID)).Scan(&cash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrPlayerNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("query player cash: %w", err)
	}
	return cash, nil
}

const adjustCashSQL = `
UPDATE players
SET cash = cash + $2
WHERE id = $1 AND cash + $2 >= 0
RETURNING cash
`

// AdjustCash applies delta atomically and returns the new balance. Negative
// delta = debit (player spends), positive = credit. The conditional UPDATE
// refuses to drop below zero so a concurrent buy can't overdraw the player.
// We re-read on no-rows to tell ErrPlayerNotFound from ErrInsufficientCash.
func (r *Repository) AdjustCash(ctx context.Context, playerID domain.PlayerID, delta int64) (int64, error) {
	var newCash int64
	err := r.exec.QueryRow(ctx, adjustCashSQL, int64(playerID), delta).Scan(&newCash)
	if err == nil {
		return newCash, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("adjust player cash: %w", err)
	}
	// No-rows: either the player vanished or the delta would overdraw.
	current, err := r.GetCash(ctx, playerID)
	if err != nil {
		return 0, err
	}
	if current+delta < 0 {
		return 0, ErrInsufficientCash
	}
	return 0, fmt.Errorf("adjust cash: unexpected no-rows update for player=%d cash=%d delta=%d", playerID, current, delta)
}

const getActiveShipSQL = `SELECT active_ship_id FROM players WHERE id = $1`

// ActiveShip returns the player's explicitly-selected active ship (phase
// 10.14a). ok is false when active_ship_id is NULL — callers then fall back
// to the lowest-id rule. ErrPlayerNotFound when the player row is missing.
func (r *Repository) ActiveShip(ctx context.Context, playerID domain.PlayerID) (domain.ShipID, bool, error) {
	var id *int64
	err := r.exec.QueryRow(ctx, getActiveShipSQL, int64(playerID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, ErrPlayerNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("query active ship: %w", err)
	}
	if id == nil {
		return 0, false, nil
	}
	return domain.ShipID(*id), true, nil
}

const setActiveShipSQL = `UPDATE players SET active_ship_id = $2 WHERE id = $1`

// SetActiveShip points the player at the ship they now control (phase 10.14a).
// A zero shipID stores NULL, reverting to the lowest-id fallback (used when a
// player becomes a passenger and has no own ship in the world).
func (r *Repository) SetActiveShip(ctx context.Context, playerID domain.PlayerID, shipID domain.ShipID) error {
	var arg *int64
	if shipID != 0 {
		v := int64(shipID)
		arg = &v
	}
	if _, err := r.exec.Exec(ctx, setActiveShipSQL, int64(playerID), arg); err != nil {
		return fmt.Errorf("set active ship: %w", err)
	}
	return nil
}

// Reputation is a player's standing across the three rating axes StarWind
// gates equipment ranks on (phase 10.3.3). All default to 0 (lowest rank).
type Reputation struct {
	War   int // combat reputation (kills); StarWind users.warstatus
	Trade int // trade reputation (deals); StarWind users.tradestatus
	Race  int // standing with the NPC races
}

const getReputationSQL = `SELECT war_rate, trade_rate, race_rate FROM players WHERE id = $1`

// GetReputation returns the player's war/trade/race reputation. The install
// rank gate (phase 10.3.4) reads these to compare against a module's
// min_war_rate / min_trade_rate / min_race_rate. ErrPlayerNotFound when the
// row is missing.
func (r *Repository) GetReputation(ctx context.Context, playerID domain.PlayerID) (Reputation, error) {
	var rep Reputation
	err := r.exec.QueryRow(ctx, getReputationSQL, int64(playerID)).Scan(&rep.War, &rep.Trade, &rep.Race)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reputation{}, ErrPlayerNotFound
	}
	if err != nil {
		return Reputation{}, fmt.Errorf("query player reputation: %w", err)
	}
	return rep, nil
}

const addReputationSQL = `
UPDATE players
SET war_rate = war_rate + $2, trade_rate = trade_rate + $3, race_rate = race_rate + $4
WHERE id = $1
RETURNING war_rate, trade_rate, race_rate
`

// AddReputation applies the deltas atomically and returns the new standing
// (phase 10.3.3). Accrual sources (kills -> War, deals -> Trade, race relations
// -> Race) call this; mirrors AdjustCash. ErrPlayerNotFound when the row is
// missing.
func (r *Repository) AddReputation(ctx context.Context, playerID domain.PlayerID, delta Reputation) (Reputation, error) {
	var rep Reputation
	err := r.exec.QueryRow(ctx, addReputationSQL, int64(playerID), delta.War, delta.Trade, delta.Race).
		Scan(&rep.War, &rep.Trade, &rep.Race)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reputation{}, ErrPlayerNotFound
	}
	if err != nil {
		return Reputation{}, fmt.Errorf("add player reputation: %w", err)
	}
	return rep, nil
}

const getPassengerHostSQL = `SELECT passenger_of_ship_id FROM players WHERE id = $1`

// PassengerHost returns the host ship the player currently rides as a passenger
// (phase 10.23). ok is false when passenger_of_ship_id is NULL (not a
// passenger). ErrPlayerNotFound when the row is missing.
func (r *Repository) PassengerHost(ctx context.Context, playerID domain.PlayerID) (domain.ShipID, bool, error) {
	var id *int64
	err := r.exec.QueryRow(ctx, getPassengerHostSQL, int64(playerID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, ErrPlayerNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("query passenger host: %w", err)
	}
	if id == nil {
		return 0, false, nil
	}
	return domain.ShipID(*id), true, nil
}

const setPassengerHostSQL = `UPDATE players SET passenger_of_ship_id = $2 WHERE id = $1`

// SetPassengerHost records (or clears, with hostID==0) the host ship a player
// rides as a passenger (phase 10.23). Cleared on disembark / host death.
func (r *Repository) SetPassengerHost(ctx context.Context, playerID domain.PlayerID, hostID domain.ShipID) error {
	var arg *int64
	if hostID != 0 {
		v := int64(hostID)
		arg = &v
	}
	if _, err := r.exec.Exec(ctx, setPassengerHostSQL, int64(playerID), arg); err != nil {
		return fmt.Errorf("set passenger host: %w", err)
	}
	return nil
}

// PassengerLink ties a passenger player to the host ship they ride (phase
// 10.23). Used at sector cold-start to rebuild each host's RAM PassengerPlayers.
type PassengerLink struct {
	PlayerID   domain.PlayerID
	HostShipID domain.ShipID
}

const passengerLinksSQL = `SELECT id, passenger_of_ship_id FROM players WHERE passenger_of_ship_id IS NOT NULL`

// PassengerLinks returns every active passenger→host link (phase 10.23). The
// app rebuilds host PassengerPlayers from these when workers cold-start, since
// the mirror is RAM-only.
func (r *Repository) PassengerLinks(ctx context.Context) ([]PassengerLink, error) {
	rows, err := r.exec.Query(ctx, passengerLinksSQL)
	if err != nil {
		return nil, fmt.Errorf("query passenger links: %w", err)
	}
	defer rows.Close()

	var out []PassengerLink
	for rows.Next() {
		var pid, host int64
		if err := rows.Scan(&pid, &host); err != nil {
			return nil, fmt.Errorf("scan passenger link: %w", err)
		}
		out = append(out, PassengerLink{PlayerID: domain.PlayerID(pid), HostShipID: domain.ShipID(host)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate passenger links: %w", err)
	}
	return out, nil
}

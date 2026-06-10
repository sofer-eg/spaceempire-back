// Package rent implements ownership upkeep (phase 6.4): players owe periodic
// rent on the stations/shipyards/trade-stations they own, charged by a
// background Closer; non-payment past a limit confiscates the object (owner
// cleared to NPC/gov). Reinterpretation of SP TO_RentCheck — see
// back/docs/specs/rent.md.
package rent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
	stationsrepo "spaceempire/back/internal/persistence/stations"
	"spaceempire/back/internal/pkg/clock"
)

var (
	// ErrStationOwned is returned by Claim when the station is already owned.
	ErrStationOwned = errors.New("rent: station already owned")
	// ErrInsufficientFunds is returned by Claim when the player cannot pay the
	// claim price.
	ErrInsufficientFunds = errors.New("rent: insufficient funds")
)

// TxRepo is the slice of persistence ops the billing transaction needs, bound
// to one tx. Composed in tx_runner.go from the rents/players/stations repos.
type TxRepo interface {
	Due(ctx context.Context, now time.Time, limit int) ([]domain.Rent, error)
	MarkPaid(ctx context.Context, id domain.RentID, paidAt, nextDue time.Time) error
	MarkUnpaid(ctx context.Context, id domain.RentID, unpaidPeriods int, nextDue time.Time) error
	Delete(ctx context.Context, id domain.RentID) error
	AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error)
	ClearOwner(ctx context.Context, station domain.EntityRef) error
	// ClaimStation takes an unowned station for owner (8.7); claimed=false
	// when it was already owned.
	ClaimStation(ctx context.Context, station domain.EntityRef, owner domain.PlayerID) (bool, error)
	// Ensure creates the rent row for a freshly owned object (8.7).
	Ensure(ctx context.Context, payer domain.PlayerID, station domain.EntityRef, amountPerPeriod int64, nextDue time.Time) error
}

// TxRunner executes fn inside a database transaction with a TxRepo bound to it.
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, repo TxRepo) error) error
}

// RentStore is the pool-backed rent ops the reconcile + read endpoints use.
type RentStore interface {
	Ensure(ctx context.Context, payer domain.PlayerID, station domain.EntityRef, amountPerPeriod int64, nextDue time.Time) error
	ListByPayer(ctx context.Context, payer domain.PlayerID) ([]domain.Rent, error)
}

// Stations enumerates player-owned objects for the reconcile.
type Stations interface {
	PlayerOwned(ctx context.Context) ([]stationsrepo.OwnedStatic, error)
}

// Config tunes the billing.
type Config struct {
	// Period is the billing cycle: how far next_due_at advances after each
	// charge attempt.
	Period time.Duration
	// MaxUnpaid is how many consecutive missed charges trigger confiscation.
	MaxUnpaid int
	// DefaultAmount is the per-period rent assigned to auto-reconciled rents
	// (a flat upkeep until a station-acquisition feature sets a real price).
	DefaultAmount int64
	// ClaimPrice is the one-off cost to claim an unowned station (8.7).
	ClaimPrice int64
}

const (
	defaultPeriod     = 24 * time.Hour
	defaultMaxUnpaid  = 3
	defaultAmount     = 5000
	defaultClaimPrice = 100000
)

// Service charges rent, reconciles obligations against current ownership, and
// serves the per-player read.
type Service struct {
	store    RentStore
	stations Stations
	tx       TxRunner
	pub      bus.Publisher
	clock    clock.Clock
	logger   *slog.Logger
	cfg      Config
}

// New wires a Service, applying defaults to a zero Config. pub may be nil
// (overdue events are then silently skipped).
func New(store RentStore, stations Stations, tx TxRunner, pub bus.Publisher, clk clock.Clock, logger *slog.Logger, cfg Config) *Service {
	if cfg.Period <= 0 {
		cfg.Period = defaultPeriod
	}
	if cfg.MaxUnpaid <= 0 {
		cfg.MaxUnpaid = defaultMaxUnpaid
	}
	if cfg.DefaultAmount <= 0 {
		cfg.DefaultAmount = defaultAmount
	}
	if cfg.ClaimPrice <= 0 {
		cfg.ClaimPrice = defaultClaimPrice
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, stations: stations, tx: tx, pub: pub, clock: clk, logger: logger, cfg: cfg}
}

// Reconcile ensures a rent row exists for every currently player-owned
// station/shipyard/trade-station. Idempotent: existing rows keep their
// schedule. Run at startup and each billing tick so a newly acquired object
// starts owing rent without a restart.
func (s *Service) Reconcile(ctx context.Context) error {
	owned, err := s.stations.PlayerOwned(ctx)
	if err != nil {
		return err
	}
	nextDue := s.clock.Now().Add(s.cfg.Period)
	for _, o := range owned {
		if err := s.store.Ensure(ctx, o.Owner, o.Ref, s.cfg.DefaultAmount, nextDue); err != nil {
			return err
		}
	}
	return nil
}

// ProcessDue charges every rent past its due date (up to limit), in one
// transaction. A payer who cannot pay has their unpaid counter bumped; once it
// reaches MaxUnpaid the object is confiscated (owner cleared, rent deleted).
// Overdue notifications are published after the transaction commits.
func (s *Service) ProcessDue(ctx context.Context, limit int) error {
	now := s.clock.Now()
	nextDue := now.Add(s.cfg.Period)
	var events []OverdueEvent

	err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		due, err := repo.Due(ctx, now, limit)
		if err != nil {
			return err
		}
		for _, r := range due {
			_, e := repo.AdjustCash(ctx, r.Payer, -r.AmountPerPeriod)
			switch {
			case e == nil:
				if err := repo.MarkPaid(ctx, r.ID, now, nextDue); err != nil {
					return err
				}
			case errors.Is(e, playersrepo.ErrInsufficientCash):
				count := r.UnpaidPeriods + 1
				ev := OverdueEvent{
					RentID:          r.ID,
					Payer:           r.Payer,
					Station:         r.Station,
					AmountPerPeriod: r.AmountPerPeriod,
					UnpaidPeriods:   count,
				}
				if count >= s.cfg.MaxUnpaid {
					if err := repo.ClearOwner(ctx, r.Station); err != nil {
						return err
					}
					if err := repo.Delete(ctx, r.ID); err != nil {
						return err
					}
					ev.Confiscated = true
				} else if err := repo.MarkUnpaid(ctx, r.ID, count, nextDue); err != nil {
					return err
				}
				events = append(events, ev)
			default:
				return e
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, ev := range events {
		s.publishOverdue(ctx, ev)
	}
	if len(events) > 0 {
		s.logger.InfoContext(ctx, "rent overdue", "count", len(events))
	}
	return nil
}

// MyRents returns the rents a player owes.
func (s *Service) MyRents(ctx context.Context, player domain.PlayerID) ([]domain.Rent, error) {
	return s.store.ListByPayer(ctx, player)
}

// Claim lets a player take an unowned station for ClaimPrice, immediately
// creating its rent obligation (8.7 — the upstream that populates rents). In
// one transaction: take the station (only if unowned), debit the price, and
// Ensure the rent row. Returns ErrStationOwned / ErrInsufficientFunds; the
// whole tx rolls back on either.
func (s *Service) Claim(ctx context.Context, player domain.PlayerID, stationID domain.StationID) error {
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(stationID)}
	nextDue := s.clock.Now().Add(s.cfg.Period)
	return s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		claimed, err := repo.ClaimStation(ctx, station, player)
		if err != nil {
			return err
		}
		if !claimed {
			return ErrStationOwned
		}
		if _, err := repo.AdjustCash(ctx, player, -s.cfg.ClaimPrice); err != nil {
			if errors.Is(err, playersrepo.ErrInsufficientCash) {
				return ErrInsufficientFunds
			}
			return err
		}
		return repo.Ensure(ctx, player, station, s.cfg.DefaultAmount, nextDue)
	})
}

// publishOverdue best-effort delivers an OverdueEvent to the payer's WS topic.
func (s *Service) publishOverdue(ctx context.Context, ev OverdueEvent) {
	if s.pub == nil {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		s.logger.ErrorContext(ctx, "rent: marshal overdue", "err", err, "rent", int64(ev.RentID))
		return
	}
	if err := s.pub.Publish(ctx, OverdueTopic(ev.Payer), payload); err != nil {
		s.logger.ErrorContext(ctx, "rent: publish overdue", "err", err, "payer", int64(ev.Payer))
	}
}

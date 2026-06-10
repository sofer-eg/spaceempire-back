// Package insurance implements ship destruction cover (phase 6.5): a player
// buys a policy on a docked ship; if it is destroyed while the policy is
// active and unexpired, the holder is paid the coverage. Reinterpretation of
// the old insure.php — see back/docs/specs/insurance.md.
package insurance

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/domain"
	insurancerepo "spaceempire/back/internal/persistence/insurance"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/clock"
)

// TxRepo is the slice of persistence ops the buy/payout transactions need.
type TxRepo interface {
	ExpireActiveForShip(ctx context.Context, shipID domain.ShipID, now time.Time) error
	Create(ctx context.Context, p domain.InsurancePolicy) (domain.PolicyID, error)
	ActiveForShip(ctx context.Context, shipID domain.ShipID, now time.Time) (domain.InsurancePolicy, bool, error)
	Claim(ctx context.Context, id domain.PolicyID, claimedAt time.Time) error
	AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error)
}

// TxRunner executes fn inside a database transaction with a TxRepo bound to it.
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, repo TxRepo) error) error
}

// Reader is the pool-backed reads: the buy authorization + the list endpoint.
type Reader interface {
	ListByPlayer(ctx context.Context, player domain.PlayerID) ([]domain.InsurancePolicy, error)
	ShipOwnership(ctx context.Context, shipID domain.ShipID) (domain.PlayerID, *domain.EntityRef, error)
}

// Config tunes the policy economics.
type Config struct {
	// CoverageMultiplier sets the payout as a multiple of the premium
	// (coverage = premium × multiplier). The premium is the player's stake;
	// the multiplier is the house's risk markup inverse.
	CoverageMultiplier int64
	// MaxDurationDays caps a policy's length; 0 = the default.
	MaxDurationDays int
}

const (
	defaultCoverageMultiplier = 10
	defaultMaxDurationDays    = 90
)

// Service exposes Buy, the payout-on-kill, and the per-player read.
type Service struct {
	read   Reader
	tx     TxRunner
	clock  clock.Clock
	logger *slog.Logger
	cfg    Config
}

// New wires a Service, applying defaults to a zero Config.
func New(read Reader, tx TxRunner, clk clock.Clock, logger *slog.Logger, cfg Config) *Service {
	if cfg.CoverageMultiplier <= 0 {
		cfg.CoverageMultiplier = defaultCoverageMultiplier
	}
	if cfg.MaxDurationDays <= 0 {
		cfg.MaxDurationDays = defaultMaxDurationDays
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{read: read, tx: tx, clock: clk, logger: logger, cfg: cfg}
}

// Buy charges the premium and writes an active policy covering the ship for
// durationDays. The caller must own the ship and it must be docked (commerce
// at a station). Coverage = premium × CoverageMultiplier.
func (s *Service) Buy(ctx context.Context, player domain.PlayerID, shipID domain.ShipID, premium int64, durationDays int) (domain.PolicyID, error) {
	if premium <= 0 || durationDays <= 0 {
		return 0, ErrInvalidInput
	}
	if durationDays > s.cfg.MaxDurationDays {
		durationDays = s.cfg.MaxDurationDays
	}
	owner, docked, err := s.read.ShipOwnership(ctx, shipID)
	if err != nil {
		if errors.Is(err, insurancerepo.ErrShipNotFound) {
			return 0, ErrShipNotFound
		}
		return 0, err
	}
	if owner != player {
		return 0, ErrNotOwner
	}
	if docked == nil {
		return 0, ErrNotDocked
	}

	now := s.clock.Now()
	policy := domain.InsurancePolicy{
		ShipID:      shipID,
		PlayerID:    player,
		PremiumPaid: premium,
		Coverage:    premium * s.cfg.CoverageMultiplier,
		Status:      domain.PolicyActive,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Duration(durationDays) * 24 * time.Hour),
	}

	var id domain.PolicyID
	err = s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		// Lazy-expire a time-lapsed policy so the active-per-ship unique index
		// does not block a re-insure.
		if e := repo.ExpireActiveForShip(ctx, shipID, now); e != nil {
			return e
		}
		if _, e := repo.AdjustCash(ctx, player, -premium); e != nil {
			if errors.Is(e, playersrepo.ErrInsufficientCash) {
				return ErrInsufficientFunds
			}
			return e
		}
		var e error
		id, e = repo.Create(ctx, policy)
		if errors.Is(e, insurancerepo.ErrAlreadyInsured) {
			return ErrAlreadyInsured
		}
		return e
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// OnKill pays out the ship's active policy (if any) to its holder and marks it
// claimed, in one transaction. No-op when the ship is not insured.
func (s *Service) OnKill(ctx context.Context, shipID domain.ShipID) error {
	now := s.clock.Now()
	return s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		policy, ok, err := repo.ActiveForShip(ctx, shipID, now)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if _, err := repo.AdjustCash(ctx, policy.PlayerID, policy.Coverage); err != nil {
			return err
		}
		if err := repo.Claim(ctx, policy.ID, now); err != nil {
			return err
		}
		s.logger.InfoContext(ctx, "insurance payout",
			"policy", int64(policy.ID), "ship", int64(shipID),
			"holder", int64(policy.PlayerID), "coverage", policy.Coverage)
		return nil
	})
}

// MyPolicies returns every policy the player holds.
func (s *Service) MyPolicies(ctx context.Context, player domain.PlayerID) ([]domain.InsurancePolicy, error) {
	return s.read.ListByPlayer(ctx, player)
}

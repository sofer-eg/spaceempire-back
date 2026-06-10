// Package bounties implements the player/clan-driven bounty feature (phase
// 6.3): a sponsor escrows credits on a target's head, the killer collects on
// a claim, the sponsor is refunded on expiry. The Service owns the escrow
// logic; persistence/bounties does the storage. See
// back/docs/specs/bounties.md.
package bounties

import (
	"context"
	"errors"
	"log/slog"
	"time"

	bountyrepo "spaceempire/back/internal/persistence/bounties"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/clock"
	clansrepo "spaceempire/back/internal/social/clans"

	"spaceempire/back/internal/domain"
)

// TxRepo is the slice of persistence ops the escrow transactions need, bound
// to a single transaction. Composed in tx_runner.go from the bounty, players,
// and clans repositories. Declared here (ISP) so service tests stub it.
type TxRepo interface {
	CreateBounty(ctx context.Context, b domain.Bounty) (domain.BountyID, error)
	ActiveForTargets(ctx context.Context, now time.Time, targets []domain.EntityRef) ([]domain.Bounty, error)
	DueExpired(ctx context.Context, now time.Time, limit int) ([]domain.Bounty, error)
	MarkPaid(ctx context.Context, id domain.BountyID, paidTo domain.PlayerID, at time.Time) error
	MarkExpired(ctx context.Context, id domain.BountyID) error
	AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error)
	AdjustTreasury(ctx context.Context, c domain.ClanID, delta int64) (int64, error)
}

// TxRunner executes fn inside a database transaction with a TxRepo bound to it.
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, repo TxRepo) error) error
}

// Reader is the pool-backed read ops for the list/history endpoints.
type Reader interface {
	ListActive(ctx context.Context, now time.Time, limit int) ([]bountyrepo.View, error)
	HistoryForTarget(ctx context.Context, target domain.EntityRef, limit int) ([]bountyrepo.View, error)
}

// Clans resolves clan membership/leadership for clan-targeted/-funded
// bounties.
type Clans interface {
	// ClanOf returns the player's clan and ok=false when they are in none.
	ClanOf(ctx context.Context, player domain.PlayerID) (domain.ClanID, bool, error)
	// LeaderOf returns the leader of a clan.
	LeaderOf(ctx context.Context, clan domain.ClanID) (domain.PlayerID, error)
}

// Config tunes the Service.
type Config struct {
	// DefaultTTL is applied when SetBounty is called with a non-positive ttl.
	DefaultTTL time.Duration
	// TopLimit caps how many bounties the active-list endpoint returns.
	TopLimit int
	// HistoryLimit caps the per-target history endpoint.
	HistoryLimit int
	// IgnoreKiller, when non-zero, is a player whose kills never claim a
	// bounty — the __npc__ system player. An NPC killing a hunted target
	// leaves the bounty open for a real hunter instead of paying the NPC
	// account (6.3 follow-up). 0 disables the guard.
	IgnoreKiller domain.PlayerID
}

const (
	defaultTTL          = 7 * 24 * time.Hour
	defaultTopLimit     = 50
	defaultHistoryLimit = 100
)

// Service exposes the bounty operations: set, payout-on-kill, expiry, and the
// two read views.
type Service struct {
	read   Reader
	tx     TxRunner
	clans  Clans
	clock  clock.Clock
	logger *slog.Logger
	cfg    Config
}

// New wires a Service, applying defaults to a zero Config.
func New(read Reader, tx TxRunner, clans Clans, clk clock.Clock, logger *slog.Logger, cfg Config) *Service {
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = defaultTTL
	}
	if cfg.TopLimit <= 0 {
		cfg.TopLimit = defaultTopLimit
	}
	if cfg.HistoryLimit <= 0 {
		cfg.HistoryLimit = defaultHistoryLimit
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{read: read, tx: tx, clans: clans, clock: clk, logger: logger, cfg: cfg}
}

// SetBounty escrows amount on target's head and records an active bounty.
// caller is the authenticated player; fromClan funds it from the caller's
// clan treasury (caller must be the leader) instead of their personal cash.
func (s *Service) SetBounty(ctx context.Context, caller domain.PlayerID, target domain.EntityRef, amount int64, ttl time.Duration, fromClan bool) (domain.BountyID, error) {
	if amount <= 0 || !validKind(target.Kind) {
		return 0, ErrInvalidInput
	}
	sponsor, err := s.resolveSponsor(ctx, caller, fromClan)
	if err != nil {
		return 0, err
	}
	if sponsor == target {
		return 0, ErrSelfBounty
	}
	if ttl <= 0 {
		ttl = s.cfg.DefaultTTL
	}
	now := s.clock.Now()
	b := domain.Bounty{
		Target:    target,
		Sponsor:   sponsor,
		Amount:    amount,
		Status:    domain.BountyActive,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	var id domain.BountyID
	err = s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		if e := s.debit(ctx, repo, sponsor, amount); e != nil {
			return e
		}
		var e error
		id, e = repo.CreateBounty(ctx, b)
		return e
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// resolveSponsor returns the funding entity: the caller's clan (leader-only)
// when fromClan, otherwise the caller themselves.
func (s *Service) resolveSponsor(ctx context.Context, caller domain.PlayerID, fromClan bool) (domain.EntityRef, error) {
	if !fromClan {
		return domain.PlayerRef(caller), nil
	}
	clanID, ok, err := s.clans.ClanOf(ctx, caller)
	if err != nil {
		return domain.EntityRef{}, err
	}
	if !ok {
		return domain.EntityRef{}, ErrNotInClan
	}
	leader, err := s.clans.LeaderOf(ctx, clanID)
	if err != nil {
		return domain.EntityRef{}, err
	}
	if leader != caller {
		return domain.EntityRef{}, ErrNotClanLeader
	}
	return domain.ClanRef(clanID), nil
}

// OnKill pays out every active bounty on the victim (and their clan) to the
// killer, in one transaction. No-op when the killer is unknown, the victim is
// not a player, or it is a self-kill (the task's own-kill exclusion).
func (s *Service) OnKill(ctx context.Context, killer, victim domain.PlayerID) error {
	if killer == 0 || victim == 0 || killer == victim {
		return nil
	}
	// NPC/environmental killers do not collect — leave the bounty open for a
	// real hunter (6.3 follow-up).
	if s.cfg.IgnoreKiller != 0 && killer == s.cfg.IgnoreKiller {
		return nil
	}
	targets := []domain.EntityRef{domain.PlayerRef(victim)}
	clanID, ok, err := s.clans.ClanOf(ctx, victim)
	if err != nil {
		return err
	}
	if ok {
		targets = append(targets, domain.ClanRef(clanID))
	}
	now := s.clock.Now()
	return s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		bs, err := repo.ActiveForTargets(ctx, now, targets)
		if err != nil {
			return err
		}
		var total int64
		for _, b := range bs {
			if _, e := repo.AdjustCash(ctx, killer, b.Amount); e != nil {
				return e
			}
			if e := repo.MarkPaid(ctx, b.ID, killer, now); e != nil {
				return e
			}
			total += b.Amount
		}
		if total > 0 {
			s.logger.InfoContext(ctx, "bounty payout",
				"killer", int64(killer), "victim", int64(victim),
				"bounties", len(bs), "total", total)
		}
		return nil
	})
}

// ExpireDue refunds and closes up to limit bounties past their deadline,
// returning how many were expired. Called by the Closer each tick.
func (s *Service) ExpireDue(ctx context.Context, limit int) (int, error) {
	now := s.clock.Now()
	var n int
	err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		bs, err := repo.DueExpired(ctx, now, limit)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if e := s.refund(ctx, repo, b.Sponsor, b.Amount); e != nil {
				return e
			}
			if e := repo.MarkExpired(ctx, b.ID); e != nil {
				return e
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.logger.InfoContext(ctx, "bounties expired", "count", n)
	}
	return n, nil
}

// TopActive returns the highest-value active bounties for GET /api/bounties.
func (s *Service) TopActive(ctx context.Context) ([]bountyrepo.View, error) {
	return s.read.ListActive(ctx, s.clock.Now(), s.cfg.TopLimit)
}

// History returns every bounty ever targeting the player, newest first.
func (s *Service) History(ctx context.Context, player domain.PlayerID) ([]bountyrepo.View, error) {
	return s.read.HistoryForTarget(ctx, domain.PlayerRef(player), s.cfg.HistoryLimit)
}

// debit removes amount from the sponsor wallet, mapping the repo's
// insufficiency sentinels to ErrInsufficientFunds.
func (s *Service) debit(ctx context.Context, repo TxRepo, who domain.EntityRef, amount int64) error {
	switch who.Kind {
	case domain.EntityKindPlayer:
		_, err := repo.AdjustCash(ctx, domain.PlayerID(who.ID), -amount)
		if errors.Is(err, playersrepo.ErrInsufficientCash) {
			return ErrInsufficientFunds
		}
		return err
	case domain.EntityKindClan:
		_, err := repo.AdjustTreasury(ctx, domain.ClanID(who.ID), -amount)
		if errors.Is(err, clansrepo.ErrInsufficientTreasury) {
			return ErrInsufficientFunds
		}
		return err
	default:
		return ErrInvalidInput
	}
}

// refund returns amount to the sponsor wallet. A positive delta never trips
// an insufficiency guard.
func (s *Service) refund(ctx context.Context, repo TxRepo, who domain.EntityRef, amount int64) error {
	switch who.Kind {
	case domain.EntityKindPlayer:
		_, err := repo.AdjustCash(ctx, domain.PlayerID(who.ID), amount)
		return err
	case domain.EntityKindClan:
		_, err := repo.AdjustTreasury(ctx, domain.ClanID(who.ID), amount)
		return err
	default:
		return ErrInvalidInput
	}
}

// validKind reports whether k is a legal bounty target/sponsor kind.
func validKind(k domain.EntityKind) bool {
	return k == domain.EntityKindPlayer || k == domain.EntityKindClan
}

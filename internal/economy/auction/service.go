package auction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"spaceempire/back/internal/domain"
	auctionrepo "spaceempire/back/internal/persistence/auction"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/clock"
)

// MinDuration / MaxDuration cap how long a lot can sit open. Tight floor
// keeps the closer responsive in tests; week-long ceiling matches the
// design's expectation of overnight markets.
const (
	MinDuration = time.Minute
	MaxDuration = 7 * 24 * time.Hour
)

// Repo is the union of persistence operations Service needs. Defined here
// (per ISP) so unit tests can stub it without importing pgx.
type Repo interface {
	// auction lots / bids
	CreateLot(ctx context.Context, p auctionrepo.CreateLotParams) (auctionrepo.Lot, error)
	GetLot(ctx context.Context, id int64) (auctionrepo.Lot, error)
	LockLot(ctx context.Context, id int64) (auctionrepo.Lot, error)
	ListActive(ctx context.Context) ([]auctionrepo.Lot, error)
	ListByParticipant(ctx context.Context, player domain.PlayerID, limit int) ([]auctionrepo.Lot, error)
	ListDue(ctx context.Context, now time.Time, limit int) ([]int64, error)
	UpdateBid(ctx context.Context, lotID int64, newPrice int64, bidder domain.PlayerID) error
	InsertBid(ctx context.Context, lotID int64, bidder domain.PlayerID, amount int64) error
	SetStatus(ctx context.Context, lotID int64, status auctionrepo.Status) error
	FindDeliveryShip(ctx context.Context, buyer domain.PlayerID, requiredSpace float64) (domain.ShipID, bool, error)

	// ships — dock authorization
	LoadShipDock(ctx context.Context, shipID domain.ShipID) (auctionrepo.ShipDock, error)

	// players / cargo
	AdjustCash(ctx context.Context, playerID domain.PlayerID, delta int64) (int64, error)
	AddCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	SubtractCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	GoodsType(ctx context.Context, id domain.GoodsTypeID) (domain.GoodsType, error)
}

// TxRunner executes fn inside one pgx transaction and hands fn a Repo bound
// to that transaction. RepoTxRunner provides the production wiring.
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error
}

// Service exposes Create / Bid / Close — the public surface used by the
// HTTP handlers and the background closer.
type Service struct {
	tx     TxRunner
	clock  clock.Clock
	logger *slog.Logger
}

// New wires a Service. clock drives ends_at and Close's "due" check;
// passing a FakeClock in tests advances time deterministically.
func New(tx TxRunner, clk clock.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{tx: tx, clock: clk, logger: logger}
}

// CreateParams collects every input Create needs in one struct so HTTP
// handlers and tests don't repeat positional argument lists.
type CreateParams struct {
	Seller     domain.PlayerID
	Source     domain.EntityRef
	GoodsType  domain.GoodsTypeID
	Quantity   int64
	StartPrice int64
	Duration   time.Duration
}

// Create validates input, debits the seller's cargo, and inserts the lot.
// Whole operation runs in one transaction so a half-created lot cannot
// exist (cargo subtracted but lot row missing).
func (s *Service) Create(ctx context.Context, p CreateParams) (auctionrepo.Lot, error) {
	if p.Quantity <= 0 {
		return auctionrepo.Lot{}, ErrInvalidQuantity
	}
	if p.StartPrice <= 0 {
		return auctionrepo.Lot{}, ErrInvalidStartPrice
	}
	if p.Duration < MinDuration || p.Duration > MaxDuration {
		return auctionrepo.Lot{}, ErrInvalidDuration
	}

	now := s.clock.Now()
	endsAt := now.Add(p.Duration)

	var lot auctionrepo.Lot
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		// Lots are listed from a docked ship's hold (the X-Tension model:
		// commerce only at a dock). Authorize before touching cargo.
		if p.Source.Kind == domain.EntityKindShip {
			if err := s.requireDocked(ctx, txRepo, p.Seller, domain.ShipID(p.Source.ID)); err != nil {
				return err
			}
		}
		if err := txRepo.SubtractCargo(ctx, p.Source, p.GoodsType, p.Quantity); err != nil {
			if errors.Is(err, cargorepo.ErrInsufficientQuantity) {
				return ErrInsufficientCargo
			}
			return fmt.Errorf("debit seller cargo: %w", err)
		}
		created, err := txRepo.CreateLot(ctx, auctionrepo.CreateLotParams{
			SellerID:   p.Seller,
			GoodsType:  p.GoodsType,
			Quantity:   p.Quantity,
			Source:     p.Source,
			StartPrice: p.StartPrice,
			EndsAt:     endsAt,
		})
		if err != nil {
			return fmt.Errorf("create lot: %w", err)
		}
		lot = created
		return nil
	})
	if err != nil {
		return auctionrepo.Lot{}, err
	}
	return lot, nil
}

// BidResult mirrors the API contract: the new price and whether the
// caller is now the leader (always true on success).
type BidResult struct {
	NewPrice  int64
	NewLeader bool
}

// Bid places a single bid. Atomically: lock the lot, validate, escrow the
// bidder's cash (debit), refund the previous leader, update price and
// leader, append to auction_bids.
func (s *Service) Bid(ctx context.Context, bidder domain.PlayerID, shipID domain.ShipID, lotID int64, amount int64) (BidResult, error) {
	if amount <= 0 {
		return BidResult{}, ErrBidTooLow
	}

	var result BidResult
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		// Bidding is a market interaction — require the bidder's ship to be
		// docked at a station (any station; lots are global).
		if err := s.requireDocked(ctx, txRepo, bidder, shipID); err != nil {
			return err
		}
		lot, err := txRepo.LockLot(ctx, lotID)
		if err != nil {
			return err
		}
		if lot.SellerID == bidder {
			return ErrSellerBid
		}
		if !s.clock.Now().Before(lot.EndsAt) {
			return ErrLotNotActive
		}
		if amount <= lot.CurrentPrice {
			return ErrBidTooLow
		}

		// Escrow: debit new bidder by the full amount.
		if _, err := txRepo.AdjustCash(ctx, bidder, -amount); err != nil {
			if errors.Is(err, playersrepo.ErrInsufficientCash) {
				return ErrInsufficientCash
			}
			return fmt.Errorf("debit bidder cash: %w", err)
		}
		// Refund the previous leader (if any). Skip the no-op self-overlap:
		// when the same bidder raises their own bid, the previous escrow
		// was already debited from them and must be returned.
		if lot.CurrentBidderID != nil {
			if _, err := txRepo.AdjustCash(ctx, *lot.CurrentBidderID, lot.CurrentPrice); err != nil {
				return fmt.Errorf("refund previous leader: %w", err)
			}
		}

		if err := txRepo.UpdateBid(ctx, lotID, amount, bidder); err != nil {
			return fmt.Errorf("update lot: %w", err)
		}
		if err := txRepo.InsertBid(ctx, lotID, bidder, amount); err != nil {
			return fmt.Errorf("insert bid: %w", err)
		}
		result = BidResult{NewPrice: amount, NewLeader: true}
		return nil
	})
	if err != nil {
		return BidResult{}, err
	}
	return result, nil
}

// Close finalizes one lot. Safe to call repeatedly: lots that are already
// closed/cancelled return nil without changes. Called by Closer (and tests).
//
// Outcomes:
//   - no bidder: cargo refunded to source owner, status = cancelled.
//   - has bidder, delivery ok: seller credited current_price, cargo delivered
//     to a docked ship of the winner.
//   - has bidder, no docked ship with room: no-sale — the buyer's escrow is
//     refunded and the goods returned to the seller's source (the seller is
//     NOT paid). Closed either way.
func (s *Service) Close(ctx context.Context, lotID int64) error {
	return s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		lot, err := txRepo.LockLot(ctx, lotID)
		if err != nil {
			if errors.Is(err, auctionrepo.ErrLotNotActive) {
				return nil
			}
			if errors.Is(err, auctionrepo.ErrLotNotFound) {
				return ErrLotNotFound
			}
			return fmt.Errorf("lock lot: %w", err)
		}

		if lot.CurrentBidderID == nil {
			if err := s.refundSeller(ctx, txRepo, lot); err != nil {
				s.logger.Error("auction.cargo_refund_failed",
					"lot", lot.ID, "seller", lot.SellerID, "err", err)
			}
			return txRepo.SetStatus(ctx, lotID, auctionrepo.StatusCancelled)
		}

		buyer := *lot.CurrentBidderID
		// Deliver first; the seller is paid only on a successful hand-off.
		if err := s.deliverToBuyer(ctx, txRepo, lot, buyer); err != nil {
			// No docked ship with room → make it a no-sale: refund the buyer's
			// escrow and return the goods to the seller's source, rather than
			// charging the buyer for goods they never receive (previously the
			// goods were silently lost while the buyer paid — auction "додел").
			// A richer alternative — dropping the goods into a space container
			// the buyer can pick up — needs a closer→sector bridge; deferred.
			if _, e := txRepo.AdjustCash(ctx, buyer, lot.CurrentPrice); e != nil {
				return fmt.Errorf("refund buyer: %w", e)
			}
			if e := s.refundSeller(ctx, txRepo, lot); e != nil {
				return fmt.Errorf("return goods to seller: %w", e)
			}
			s.logger.Warn("auction.delivery_failed_refunded",
				"lot", lot.ID, "buyer", buyer,
				"goods_type", lot.GoodsType, "quantity", lot.Quantity, "reason", err.Error())
			return txRepo.SetStatus(ctx, lotID, auctionrepo.StatusClosed)
		}

		if _, err := txRepo.AdjustCash(ctx, lot.SellerID, lot.CurrentPrice); err != nil {
			return fmt.Errorf("credit seller: %w", err)
		}
		return txRepo.SetStatus(ctx, lotID, auctionrepo.StatusClosed)
	})
}

// ListActive proxies to the repo. Read-only, runs on the pool (no tx).
func (s *Service) ListActive(ctx context.Context) ([]auctionrepo.Lot, error) {
	// We still bounce through the TxRunner so the same Repo type is used
	// everywhere. A nested transaction is cheap on a read query.
	var lots []auctionrepo.Lot
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		l, err := txRepo.ListActive(ctx)
		if err != nil {
			return err
		}
		lots = l
		return nil
	})
	return lots, err
}

// myLotsLimit caps the per-player "my auctions" list.
const myLotsLimit = 100

// MyLots returns the lots the player is involved in (as seller or current high
// bidder), newest first, across all statuses. Read-only.
func (s *Service) MyLots(ctx context.Context, player domain.PlayerID) ([]auctionrepo.Lot, error) {
	var lots []auctionrepo.Lot
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		l, err := txRepo.ListByParticipant(ctx, player, myLotsLimit)
		if err != nil {
			return err
		}
		lots = l
		return nil
	})
	return lots, err
}

// DueLots returns ids whose timer has fired. Used by Closer.
func (s *Service) DueLots(ctx context.Context, limit int) ([]int64, error) {
	now := s.clock.Now()
	var ids []int64
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		out, err := txRepo.ListDue(ctx, now, limit)
		if err != nil {
			return err
		}
		ids = out
		return nil
	})
	return ids, err
}

// requireDocked authorizes a Create/Bid: the ship must exist, be owned by
// the acting player, and be docked at a station. Auction lots are global,
// so any station counts — unlike trade, which pins the exact station. This
// mirrors the X-Tension model where all market interaction happens at a
// dock.
func (s *Service) requireDocked(ctx context.Context, txRepo Repo, playerID domain.PlayerID, shipID domain.ShipID) error {
	dock, err := txRepo.LoadShipDock(ctx, shipID)
	if err != nil {
		if errors.Is(err, auctionrepo.ErrShipNotFound) {
			return ErrShipNotFound
		}
		return fmt.Errorf("load ship dock: %w", err)
	}
	if dock.PlayerID != playerID {
		return ErrForbidden
	}
	if dock.Docked == nil {
		return ErrNotDocked
	}
	return nil
}

// refundSeller restores cargo to the original source owner. Used when the
// lot ends with no bidder. Failure is non-fatal — the lot still moves to
// cancelled and the caller logs the loss.
func (s *Service) refundSeller(ctx context.Context, txRepo Repo, lot auctionrepo.Lot) error {
	return txRepo.AddCargo(ctx, lot.Source, lot.GoodsType, lot.Quantity)
}

// deliverToBuyer attempts to put the goods into a docked ship of the
// buyer. Returns a non-nil error when delivery fails — the caller decides
// the loss policy (logged + dropped per task scope decision).
func (s *Service) deliverToBuyer(ctx context.Context, txRepo Repo, lot auctionrepo.Lot, buyer domain.PlayerID) error {
	gt, err := txRepo.GoodsType(ctx, lot.GoodsType)
	if err != nil {
		return fmt.Errorf("load goods_type: %w", err)
	}
	required := float64(lot.Quantity) * gt.Space
	shipID, ok, err := txRepo.FindDeliveryShip(ctx, buyer, required)
	if err != nil {
		return fmt.Errorf("find delivery ship: %w", err)
	}
	if !ok {
		return errors.New("no docked ship with enough cargobay")
	}
	dest := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(shipID)}
	if err := txRepo.AddCargo(ctx, dest, lot.GoodsType, lot.Quantity); err != nil {
		return fmt.Errorf("add cargo to buyer ship: %w", err)
	}
	return nil
}

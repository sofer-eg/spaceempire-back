package cargo

import (
	"context"
	"errors"
	"fmt"

	cargorepo "spaceempire/back/internal/persistence/cargo"

	"spaceempire/back/internal/domain"
)

// unownedGoods is the sentinel depositor id for cargo that belongs to no
// player: ship holds, container loot, NPC goods. Personal station deposits
// carry the depositing player's id instead. See migration 0050.
const unownedGoods domain.PlayerID = 0

// isStationLike reports whether a cargo owner is a static hold where the
// depositor (goods_owner_id) matters. Ship holds and containers are
// single-operator / unowned, so their goods always sit under unownedGoods.
func isStationLike(k domain.EntityKind) bool {
	switch k {
	case domain.EntityKindStation, domain.EntityKindTradeStation, domain.EntityKindPirbase:
		return true
	default:
		return false
	}
}

// Repo abstracts the persistence ops Service needs. Defined here per ISP
// so unit tests can stub it without pulling pgx in.
type Repo interface {
	GoodsType(ctx context.Context, id domain.GoodsTypeID) (domain.GoodsType, error)
	ListByOwner(ctx context.Context, owner domain.EntityRef, viewer domain.PlayerID) ([]domain.CargoItem, error)
	UsedSpace(ctx context.Context, owner domain.EntityRef) (float64, error)
	Capacity(ctx context.Context, owner domain.EntityRef) (float64, error)
	Add(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64, goodsOwner domain.PlayerID) error
	Subtract(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64, goodsOwner domain.PlayerID) error
	Quantity(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, goodsOwner domain.PlayerID) (int64, error)
	HasOthersGoods(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, viewer domain.PlayerID) (bool, error)
}

// TxRunner executes fn inside a database transaction and passes a Repo
// bound to that transaction. The injected implementation in app/ wraps
// database.TxManager + persistence/cargo.Repository.WithExecutor.
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error
}

// Service exposes the public cargo operations: read inventory, atomic
// move with capacity checks.
type Service struct {
	repo Repo
	tx   TxRunner
}

// New wires a Service. Both deps are required.
func New(repo Repo, tx TxRunner) *Service {
	return &Service{repo: repo, tx: tx}
}

// Inventory loads the owner's stacks plus its capacity / current usage.
// Reads use the pool directly — no transaction needed for a read-only
// snapshot. viewer is the requesting player: for a station hold the listing
// shows only the viewer's own deposits plus the unowned pool, hiding other
// players' goods. Used (capacity usage) is physical and counts every stack —
// the hold is shared even when its contents are not.
func (s *Service) Inventory(ctx context.Context, owner domain.EntityRef, viewer domain.PlayerID) (domain.Inventory, error) {
	capacity, err := s.repo.Capacity(ctx, owner)
	if err != nil {
		if errors.Is(err, cargorepo.ErrOwnerNotFound) {
			return domain.Inventory{}, ErrOwnerNotFound
		}
		if errors.Is(err, cargorepo.ErrUnsupportedOwnerKind) {
			return domain.Inventory{}, ErrUnsupportedOwnerKind
		}
		return domain.Inventory{}, err
	}
	used, err := s.repo.UsedSpace(ctx, owner)
	if err != nil {
		return domain.Inventory{}, err
	}
	items, err := s.repo.ListByOwner(ctx, owner, viewer)
	if err != nil {
		return domain.Inventory{}, err
	}
	return domain.Inventory{
		Owner:    owner,
		Capacity: capacity,
		Used:     used,
		Items:    items,
	}, nil
}

// Consume atomically removes qty units of gtype from the owner's
// inventory. Used by callers that destroy cargo rather than transfer it
// (e.g. missile launches in phase 4.3). Wrapped in tx so the read-modify
// is consistent with concurrent Move/Add calls.
//
// Returns ErrInsufficientQuantity when the owner does not hold enough
// of the requested goods type, ErrGoodsTypeNotFound when gtype is not
// in the catalog, ErrNonPositiveQuantity for qty<=0.
func (s *Service) Consume(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	// Checked here as well as in ConsumeIn on purpose: rejecting a nonsense qty
	// before opening a transaction costs nothing, and ConsumeIn must keep its own
	// guard because callers reach it directly with their own tx.
	if qty <= 0 {
		return ErrNonPositiveQuantity
	}
	return s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		return ConsumeIn(ctx, txRepo, owner, gtype, qty)
	})
}

// ConsumeIn is Consume's body over a caller-supplied repo, so an operation
// that must commit together with the debit can run both inside ONE
// transaction of its own. Used by the install-jammer / install-satellite path
// (TASK-144): the goods debit and the object INSERT share a transaction, which
// removes the "cargo taken but no object" / "object built but goods refunded"
// windows a two-call Consume+Refund orchestration leaves open.
//
// repo must already be bound to that transaction (persistence/cargo
// Repository.WithExecutor(tx)). Error semantics are identical to Consume.
func ConsumeIn(ctx context.Context, repo Repo, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	if qty <= 0 {
		return ErrNonPositiveQuantity
	}
	if _, err := repo.GoodsType(ctx, gtype); err != nil {
		if errors.Is(err, cargorepo.ErrGoodsTypeNotFound) {
			return ErrGoodsTypeNotFound
		}
		return err
	}
	if err := repo.Subtract(ctx, owner, gtype, qty, unownedGoods); err != nil {
		if errors.Is(err, cargorepo.ErrInsufficientQuantity) {
			return ErrInsufficientQuantity
		}
		return fmt.Errorf("subtract on consume: %w", err)
	}
	return nil
}

// FitsIn reports how many whole units of gtype the owner's hold can still take,
// from inside the caller's ALREADY OPEN transaction. It is Add's capacity check
// turned into a quantity: a caller that must not be refused, but must not overfill
// either, sizes its credit with this and pays back only what fits (TASK-156's
// partial drone recall).
//
// A weightless good (space <= 0) is unbounded by capacity, so the answer is
// limit — the caller's own ceiling — rather than a division by zero. An
// already-overfull hold answers 0, never a negative.
//
// repo must already be bound to that transaction (persistence/cargo
// Repository.WithExecutor(tx)). Errors mirror Add's: ErrGoodsTypeNotFound,
// ErrOwnerNotFound, ErrUnsupportedOwnerKind.
func FitsIn(ctx context.Context, repo Repo, owner domain.EntityRef, gtype domain.GoodsTypeID, limit int64) (int64, error) {
	gt, err := repo.GoodsType(ctx, gtype)
	if err != nil {
		if errors.Is(err, cargorepo.ErrGoodsTypeNotFound) {
			return 0, ErrGoodsTypeNotFound
		}
		return 0, err
	}
	if gt.Space <= 0 {
		return limit, nil
	}
	capacity, err := repo.Capacity(ctx, owner)
	if err != nil {
		if errors.Is(err, cargorepo.ErrOwnerNotFound) {
			return 0, ErrOwnerNotFound
		}
		if errors.Is(err, cargorepo.ErrUnsupportedOwnerKind) {
			return 0, ErrUnsupportedOwnerKind
		}
		return 0, err
	}
	used, err := repo.UsedSpace(ctx, owner)
	if err != nil {
		return 0, err
	}
	fits := int64((capacity - used) / gt.Space)
	switch {
	case fits < 0:
		return 0, nil
	case fits > limit:
		return limit, nil
	default:
		return fits, nil
	}
}

// AvailableIn reports how many whole units of gtype the owner's hold carries,
// from inside the caller's ALREADY OPEN transaction. It is FitsIn's mirror on the
// debit side (TASK-176): a caller that must not be refused, but must not overdraw
// either, sizes its debit with this and consumes only what is there.
//
// It counts the SAME stack ConsumeIn debits — the unowned pool — so a quantity it
// reports is always a quantity ConsumeIn can take. Goods another player deposited
// into a station hold are invisible here for that reason.
//
// An empty stack answers 0 and is not an error: what "nothing left" means belongs
// to the caller (the drone salvo turns it into cargo.ErrInsufficientQuantity, so
// the player still gets a 400 for an empty magazine).
//
// repo must already be bound to that transaction (persistence/cargo
// Repository.WithExecutor(tx)). An unknown gtype is ErrGoodsTypeNotFound — a
// misconfigured goods constant is a 500, and a caller that short-circuits on a
// zero would otherwise never reach ConsumeIn's own check and would report it as an
// empty hold.
func AvailableIn(ctx context.Context, repo Repo, owner domain.EntityRef, gtype domain.GoodsTypeID) (int64, error) {
	if _, err := repo.GoodsType(ctx, gtype); err != nil {
		if errors.Is(err, cargorepo.ErrGoodsTypeNotFound) {
			return 0, ErrGoodsTypeNotFound
		}
		return 0, err
	}
	qty, err := repo.Quantity(ctx, owner, gtype, unownedGoods)
	if err != nil {
		return 0, fmt.Errorf("read stack: %w", err)
	}
	return qty, nil
}

// RefundIn inserts (or grows) qty units of gtype into the owner's stack from
// inside the caller's ALREADY OPEN transaction — the mirror of ConsumeIn, for an
// operation that must commit together with the credit. Used by the recall-drones
// path (TASK-152), where the drone rows are deleted and the units credited
// together; a two-call orchestration would let a lost ack delete the drones and
// pay nothing back.
//
// It deliberately skips the capacity check that Add makes: the credit must not
// be refusable, or the caller is left holding a deleted object it cannot pay for.
// Callers for whom the "it fitted a moment ago" premise does not hold size the
// credit with FitsIn first (TASK-156) instead of asking this to refuse.
//
// There is no Service-level Refund wrapper: since TASK-152 every compensation
// left in the codebase happens inside a caller's transaction, and the wrapper had
// no production caller.
//
// repo must already be bound to that transaction (persistence/cargo
// Repository.WithExecutor(tx)). Returns ErrNonPositiveQuantity for qty<=0.
func RefundIn(ctx context.Context, repo Repo, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	if qty <= 0 {
		return ErrNonPositiveQuantity
	}
	if err := repo.Add(ctx, owner, gtype, qty, unownedGoods); err != nil {
		return fmt.Errorf("refund add: %w", err)
	}
	return nil
}

// Add inserts (or grows) qty units of gtype into the owner's stack, with a
// capacity check — unlike Refund, which assumes the qty just fit a moment
// ago. Used by NPC miners (phase 5.4) to deposit freshly drilled ore into the
// ship's hold. Returns ErrNoSpace when the addition would exceed the owner's
// capacity, ErrGoodsTypeNotFound for an unknown gtype, ErrNonPositiveQuantity
// for qty<=0.
func (s *Service) Add(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	if qty <= 0 {
		return ErrNonPositiveQuantity
	}
	return s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		gt, err := txRepo.GoodsType(ctx, gtype)
		if err != nil {
			if errors.Is(err, cargorepo.ErrGoodsTypeNotFound) {
				return ErrGoodsTypeNotFound
			}
			return err
		}
		capacity, err := txRepo.Capacity(ctx, owner)
		if err != nil {
			if errors.Is(err, cargorepo.ErrOwnerNotFound) {
				return ErrOwnerNotFound
			}
			if errors.Is(err, cargorepo.ErrUnsupportedOwnerKind) {
				return ErrUnsupportedOwnerKind
			}
			return err
		}
		used, err := txRepo.UsedSpace(ctx, owner)
		if err != nil {
			return err
		}
		if used+float64(qty)*gt.Space > capacity {
			return ErrNoSpace
		}
		if err := txRepo.Add(ctx, owner, gtype, qty, unownedGoods); err != nil {
			return fmt.Errorf("add cargo: %w", err)
		}
		return nil
	})
}

// Move transfers qty units of gtype from one owner to another on behalf of
// actor. The whole operation runs inside a single transaction so the capacity
// check, source subtraction, and destination addition cannot race.
//
// Depositor semantics (phase 10.22):
//   - Depositing into a station hold tags the stack with actor, so other
//     players cannot see or take it. An NPC haul (actor == 0) deposits into
//     the unowned pool.
//   - Withdrawing from a station hold draws from actor's own deposit first,
//     then the unowned pool. Goods that belong to a different player are
//     untouchable: a request for them yields ErrForbidden.
//   - Ship holds and containers are always unowned (single operator), so
//     their side of the move uses the unowned pool regardless of actor.
func (s *Service) Move(ctx context.Context, actor domain.PlayerID, from, to domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	if qty <= 0 {
		return ErrNonPositiveQuantity
	}
	if from == to {
		return ErrSameOwner
	}

	return s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		gt, err := txRepo.GoodsType(ctx, gtype)
		if err != nil {
			if errors.Is(err, cargorepo.ErrGoodsTypeNotFound) {
				return ErrGoodsTypeNotFound
			}
			return err
		}

		toCapacity, err := txRepo.Capacity(ctx, to)
		if err != nil {
			if errors.Is(err, cargorepo.ErrOwnerNotFound) {
				return ErrOwnerNotFound
			}
			if errors.Is(err, cargorepo.ErrUnsupportedOwnerKind) {
				return ErrUnsupportedOwnerKind
			}
			return err
		}

		toUsed, err := txRepo.UsedSpace(ctx, to)
		if err != nil {
			return err
		}

		needed := float64(qty) * gt.Space
		if toUsed+needed > toCapacity {
			return ErrNoSpace
		}

		if err := s.subtractFromSource(ctx, txRepo, actor, from, gtype, qty); err != nil {
			return err
		}

		toOwner := unownedGoods
		if isStationLike(to.Kind) {
			toOwner = actor
		}
		if err := txRepo.Add(ctx, to, gtype, qty, toOwner); err != nil {
			return fmt.Errorf("add to destination: %w", err)
		}
		return nil
	})
}

// subtractFromSource removes qty of gtype from the move source. For a player
// (actor > 0) withdrawing from a station hold it splits the draw across the
// player's own deposit and the unowned pool; if the only matching goods belong
// to other players it returns ErrForbidden, and a genuine shortfall returns
// ErrInsufficientQuantity. Every other source (ship, container, NPC actor)
// draws from the single unowned stack.
func (s *Service) subtractFromSource(ctx context.Context, txRepo Repo, actor domain.PlayerID, from domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	if !isStationLike(from.Kind) || actor == unownedGoods {
		if err := txRepo.Subtract(ctx, from, gtype, qty, unownedGoods); err != nil {
			if errors.Is(err, cargorepo.ErrInsufficientQuantity) {
				return ErrInsufficientQuantity
			}
			return fmt.Errorf("subtract from source: %w", err)
		}
		return nil
	}

	own, err := txRepo.Quantity(ctx, from, gtype, actor)
	if err != nil {
		return fmt.Errorf("read own stack: %w", err)
	}
	free, err := txRepo.Quantity(ctx, from, gtype, unownedGoods)
	if err != nil {
		return fmt.Errorf("read unowned stack: %w", err)
	}
	if own+free < qty {
		// Not enough that the player may take. Distinguish "someone else's
		// goods" (403) from a plain shortfall of takeable goods (409).
		if own+free == 0 {
			hasOthers, err := txRepo.HasOthersGoods(ctx, from, gtype, actor)
			if err != nil {
				return fmt.Errorf("check others goods: %w", err)
			}
			if hasOthers {
				return ErrForbidden
			}
		}
		return ErrInsufficientQuantity
	}

	takeOwn := own
	if takeOwn > qty {
		takeOwn = qty
	}
	if takeOwn > 0 {
		if err := txRepo.Subtract(ctx, from, gtype, takeOwn, actor); err != nil {
			return fmt.Errorf("subtract own stack: %w", err)
		}
	}
	if takeFree := qty - takeOwn; takeFree > 0 {
		if err := txRepo.Subtract(ctx, from, gtype, takeFree, unownedGoods); err != nil {
			return fmt.Errorf("subtract unowned stack: %w", err)
		}
	}
	return nil
}

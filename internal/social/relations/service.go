package relations

import (
	"context"
	"fmt"
	"sync"

	"spaceempire/back/internal/domain"
)

// Repo is the persistence surface the Service needs (ISP).
type Repo interface {
	LoadAll(ctx context.Context) ([]Row, error)
	Upsert(ctx context.Context, from, to domain.EntityRef, status domain.Relation) error
	Delete(ctx context.Context, from, to domain.EntityRef) error
}

// Memberships provides every player's clan, so the Service can resolve
// "same clan → friend" and propagate clan↔clan war to members. Satisfied by
// the clans repository.
type Memberships interface {
	LoadAllMemberships(ctx context.Context) (map[domain.PlayerID]domain.ClanID, error)
}

type relationKey struct {
	from domain.EntityRef
	to   domain.EntityRef
}

// Service resolves the effective relation between entities from an in-RAM
// cache (≤1µs), rebuilt by Precount and updated incrementally by Set. Safe
// for concurrent Get from many goroutines (sector workers, handlers).
type Service struct {
	repo        Repo
	memberships Memberships

	mu         sync.RWMutex
	declared   map[relationKey]domain.Relation
	playerClan map[domain.PlayerID]domain.ClanID
}

// New wires a Service. Call Precount before serving Get (an unprimed Service
// resolves everything to Neutral).
func New(repo Repo, memberships Memberships) *Service {
	return &Service{
		repo:        repo,
		memberships: memberships,
		declared:    map[relationKey]domain.Relation{},
		playerClan:  map[domain.PlayerID]domain.ClanID{},
	}
}

// Precount loads all declared relations and clan memberships into the RAM
// cache. Called at startup and may be re-run periodically (e.g. after clan
// membership changes, which Get otherwise resolves against a stale snapshot).
func (s *Service) Precount(ctx context.Context) error {
	rows, err := s.repo.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load relations: %w", err)
	}
	members, err := s.memberships.LoadAllMemberships(ctx)
	if err != nil {
		return fmt.Errorf("load memberships: %w", err)
	}

	declared := make(map[relationKey]domain.Relation, len(rows))
	for _, r := range rows {
		declared[relationKey{r.From, r.To}] = r.Status
	}

	s.mu.Lock()
	s.declared = declared
	s.playerClan = members
	s.mu.Unlock()
	return nil
}

// Get returns the effective relation from a to b (resolution order in
// back/docs/specs/relations.md §Резолюция). Pure RAM read.
func (s *Service) Get(a, b domain.EntityRef) domain.Relation {
	if a == b {
		return domain.RelationFriend
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	ca, aInClan := s.clanRefOf(a)
	cb, bInClan := s.clanRefOf(b)
	if aInClan && bInClan && ca == cb {
		return domain.RelationFriend
	}

	worstHostile := domain.RelationNeutral
	hostile, friend := false, false
	consider := func(x, y domain.EntityRef) {
		v, ok := s.declaredBetween(x, y)
		if !ok {
			return
		}
		if v.IsHostile() {
			hostile = true
			if v > worstHostile {
				worstHostile = v
			}
		} else if v == domain.RelationFriend {
			friend = true
		}
	}
	consider(a, b)
	if aInClan && bInClan {
		consider(ca, cb)
	}

	switch {
	case hostile:
		return worstHostile
	case friend:
		return domain.RelationFriend
	default:
		return domain.RelationNeutral
	}
}

// IsHostile is the combat/AI predicate: may a attack b?
func (s *Service) IsHostile(a, b domain.EntityRef) bool {
	return s.Get(a, b).IsHostile()
}

// Set declares (or clears) the directed relation a → b. RelationNeutral
// clears it (deletes the row); other statuses upsert. Writes through to the
// DB and updates the RAM cache. Get considers both directions, so a one-
// sided war declaration is mutual for hostility purposes.
func (s *Service) Set(ctx context.Context, a, b domain.EntityRef, status domain.Relation) error {
	if status == domain.RelationNeutral {
		if err := s.repo.Delete(ctx, a, b); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.declared, relationKey{a, b})
		s.mu.Unlock()
		return nil
	}
	if err := s.repo.Upsert(ctx, a, b, status); err != nil {
		return err
	}
	s.mu.Lock()
	s.declared[relationKey{a, b}] = status
	s.mu.Unlock()
	return nil
}

// clanRefOf returns the clan EntityRef of a player ref, when that player is
// in a clan. Non-player refs (or clanless players) return ok=false. Caller
// holds at least the read lock.
func (s *Service) clanRefOf(ref domain.EntityRef) (domain.EntityRef, bool) {
	if ref.Kind != domain.EntityKindPlayer {
		return domain.EntityRef{}, false
	}
	clan, ok := s.playerClan[domain.PlayerID(ref.ID)]
	if !ok {
		return domain.EntityRef{}, false
	}
	return domain.ClanRef(clan), true
}

// declaredBetween returns the most hostile declared status across either
// direction (x→y, y→x), or ok=false when neither is declared. Caller holds
// at least the read lock.
func (s *Service) declaredBetween(x, y domain.EntityRef) (domain.Relation, bool) {
	best, has := domain.RelationFriend, false
	if v, ok := s.declared[relationKey{x, y}]; ok {
		best, has = v, true
	}
	if v, ok := s.declared[relationKey{y, x}]; ok {
		if !has || v > best {
			best = v
		}
		has = true
	}
	return best, has
}

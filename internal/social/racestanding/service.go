package racestanding

import (
	"context"
	"fmt"
	"sync"

	"spaceempire/back/internal/domain"
)

// Repo is the persistence surface the Service needs (ISP).
type Repo interface {
	LoadAll(ctx context.Context) ([]Row, error)
	Upsert(ctx context.Context, player domain.PlayerID, race domain.RaceID, standing int) error
	DecayAll(ctx context.Context, step int) error
}

// Config tunes the standing thresholds. Zero fields fall back to defaults.
type Config struct {
	// WantedThreshold is the standing at or below which a player is "wanted"
	// by a race (its navy/police opens fire). Negative; default -10.
	WantedThreshold int
	// DecayStep is how far each Decay nudges a standing toward 0. Default 1.
	DecayStep int
}

func (c Config) withDefaults() Config {
	if c.WantedThreshold == 0 {
		c.WantedThreshold = -10
	}
	if c.DecayStep <= 0 {
		c.DecayStep = 1
	}
	return c
}

type key struct {
	player domain.PlayerID
	race   domain.RaceID
}

// Service resolves a player's standing with each race from an in-RAM cache
// (~1µs), primed by Precount and updated by Adjust/Decay. Safe for concurrent
// Get/IsWanted from many goroutines (the targeter runs in every sector's tick).
type Service struct {
	repo Repo
	cfg  Config

	mu       sync.RWMutex
	standing map[key]int
}

// New wires a Service. Call Precount before serving reads (an unprimed
// Service resolves everything to the neutral default 0).
func New(repo Repo, cfg Config) *Service {
	return &Service{
		repo:     repo,
		cfg:      cfg.withDefaults(),
		standing: map[key]int{},
	}
}

// Precount loads all stored standings into the RAM cache. Called at startup
// and again by Decay to refresh the cache from the post-decay DB state.
func (s *Service) Precount(ctx context.Context) error {
	rows, err := s.repo.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load race standings: %w", err)
	}
	m := make(map[key]int, len(rows))
	for _, r := range rows {
		m[key{r.Player, r.Race}] = r.Standing
	}
	s.mu.Lock()
	s.standing = m
	s.mu.Unlock()
	return nil
}

// Get returns the player's current standing with a race (0 when none stored).
func (s *Service) Get(player domain.PlayerID, race domain.RaceID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.standing[key{player, race}]
}

// IsWanted reports whether the player is wanted by a race (standing at or
// below WantedThreshold). The police/navy targeter overlay reads this.
func (s *Service) IsWanted(player domain.PlayerID, race domain.RaceID) bool {
	return s.Get(player, race) <= s.cfg.WantedThreshold
}

// Adjust changes the player's standing with a race by delta and returns the
// new value. The DB upsert runs under the write lock so concurrent Adjusts
// (and reads during the write) never lose an update — contention is low
// (a player is in one sector, scans are throttled, kills are discrete).
func (s *Service) Adjust(ctx context.Context, player domain.PlayerID, race domain.RaceID, delta int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{player, race}
	next := s.standing[k] + delta
	if err := s.repo.Upsert(ctx, player, race, next); err != nil {
		return 0, err
	}
	s.standing[k] = next
	return next, nil
}

// Decay nudges every standing one DecayStep toward 0 (the slow recovery to
// neutral) in a single DB statement, then refreshes the RAM cache from the
// post-decay state. Driven by a periodic Closer.
func (s *Service) Decay(ctx context.Context) error {
	if err := s.repo.DecayAll(ctx, s.cfg.DecayStep); err != nil {
		return err
	}
	return s.Precount(ctx)
}

// SnapshotForPlayer returns a copy of the player's stored standings keyed by
// race. Used by the GET /api/my/race-standings handler.
func (s *Service) SnapshotForPlayer(player domain.PlayerID) map[domain.RaceID]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[domain.RaceID]int{}
	for k, v := range s.standing {
		if k.player == player {
			out[k.race] = v
		}
	}
	return out
}

// WantedThreshold reports the configured wanted cutoff (for the API/UI).
func (s *Service) WantedThreshold() int { return s.cfg.WantedThreshold }

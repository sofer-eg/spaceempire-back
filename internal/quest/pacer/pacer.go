// Package pacer is the TASK-89 offer pacer: it turns raw player triggers (a
// dock, an inter-sector jump) into dosed quest offers. Per-player counters
// accrue toward a randomised threshold; when the threshold is reached the pacer
// generates one offer (a procedural template or, with StoryShare probability, a
// static story quest), persists it, and pushes it to the player's journal over
// the bus. Docks and jumps are independent (separate counters/thresholds).
//
// Everything is injected as a minimal ISP interface so unit tests drive the
// pacer with a fixed *rand.Rand + fake clock and hand-written fakes. See
// back/docs/specs/quest.md and the TASK-89 SRS (FR-04/FR-05/FR-06/FR-10, §5).
package pacer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/quest"
)

// CounterStore reads and writes the per-player pacer state
// (player_quest_counters). Satisfied by *quests.CounterRepository.
type CounterStore interface {
	GetCounters(ctx context.Context, player domain.PlayerID) (domain.QuestCounters, bool, error)
	UpsertCounters(ctx context.Context, c domain.QuestCounters) error
}

// OfferStore persists generated offers and counts a player's live ones (the
// pending-limit gate). Satisfied by *quests.OfferRepository.
type OfferStore interface {
	InsertOffer(ctx context.Context, o domain.QuestOffer) (int64, error)
	CountActiveOffers(ctx context.Context, player domain.PlayerID, now time.Time) (int, error)
}

// Generator builds one procedural quest for a player's location. ok=false means
// there is nothing solvable to offer (fail-closed); a non-nil error is a real
// failure (market read). Satisfied by *gen.Generator.
type Generator interface {
	Generate(ctx context.Context, playerSector domain.SectorID) (kind string, def quest.Def, ok bool, err error)
}

// StoryPicker selects a static story quest the player has not started and whose
// prerequisite is met (FR-10). ok=false when no candidate qualifies. templateID
// is the static quest id stored on the offer; def carries its title/steps for
// the journal projection.
type StoryPicker interface {
	Pick(ctx context.Context, player domain.PlayerID) (templateID string, def quest.Def, ok bool)
}

// Publisher pushes an offer frame onto the per-player journal topic. Satisfied
// by the in-memory bus (bus.Publisher).
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// Config is the pacer's slice of QuestConfig (SRS §7.5). The app maps
// config.QuestConfig onto it so this package does not depend on pkg/config.
type Config struct {
	// DocksMin/DocksMax bound the randomised dock count between dock offers.
	DocksMin, DocksMax int
	// JumpsMin/JumpsMax bound the randomised jump count between space offers.
	JumpsMin, JumpsMax int
	// MaxPendingOffers caps a player's live (un-accepted) offers.
	MaxPendingOffers int
	// StoryShare is the probability the pacer picks a static story quest instead
	// of a procedural template when it fires.
	StoryShare float64
	// OfferTTL is how long a generated offer lives before the Closer purges it.
	OfferTTL time.Duration
}

// triggerKind selects which counter/threshold/source a trigger drives.
type triggerKind int

const (
	triggerDock triggerKind = iota
	triggerJump
)

// Pacer dispatches trigger events into dosed offers. A single mutex serialises
// OnDock/OnJump: they run on separate bus-subscriber goroutines yet share the
// rng, the per-player counter row (read-modify-write) and the injected
// generator/picker rng, so the lock makes each trigger's whole cycle atomic.
type Pacer struct {
	counters  CounterStore
	offers    OfferStore
	generator Generator
	story     StoryPicker
	publisher Publisher
	clock     clock.Clock
	rng       *rand.Rand
	cfg       Config
	logger    *slog.Logger

	mu sync.Mutex
}

// New wires a Pacer. rng seeds threshold rolls and the story-vs-procedural
// choice — inject a fixed-seed *rand.Rand in tests for determinism.
func New(
	counters CounterStore,
	offers OfferStore,
	generator Generator,
	story StoryPicker,
	publisher Publisher,
	clk clock.Clock,
	rng *rand.Rand,
	cfg Config,
	logger *slog.Logger,
) *Pacer {
	return &Pacer{
		counters:  counters,
		offers:    offers,
		generator: generator,
		story:     story,
		publisher: publisher,
		clock:     clk,
		rng:       rng,
		cfg:       cfg,
		logger:    logger.With("component", "quest.pacer"),
	}
}

// trigger applies the shared pacing logic for one dock/jump event. docks and
// jumps share this body, differing only in which counter/threshold/range they
// drive and the source tag on the resulting offer.
func (p *Pacer) trigger(ctx context.Context, player domain.PlayerID, playerSector domain.SectorID, kind triggerKind) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	counters, _, err := p.counters.GetCounters(ctx, player)
	if err != nil {
		return fmt.Errorf("get counters: %w", err)
	}
	counters.Player = player // absent row reads as zero value with Player 0

	count, threshold, tmin, tmax := p.fields(&counters, kind)

	*count++
	if *threshold == 0 {
		*threshold = p.randRange(tmin, tmax)
	}
	if *count < *threshold {
		return p.upsert(ctx, counters) // silence until the threshold is met
	}

	now := p.clock.Now()
	active, err := p.offers.CountActiveOffers(ctx, player, now)
	if err != nil {
		return fmt.Errorf("count active offers: %w", err)
	}
	if active >= p.cfg.MaxPendingOffers {
		// AC-1b: at the live-offer cap, skip generation but still consume the
		// threshold (reset + re-roll) so the next offer waits a full interval.
		return p.resetAndUpsert(ctx, &counters, count, threshold, tmin, tmax)
	}

	templateID, def, definition, ok, err := p.produce(ctx, player, playerSector)
	if err != nil {
		return err // real failure: leave the counter unpersisted, retry next trigger
	}
	if !ok {
		// Fail-closed (AC-3b): nothing solvable / no story candidate — the
		// attempt burns, threshold re-rolled.
		return p.resetAndUpsert(ctx, &counters, count, threshold, tmin, tmax)
	}

	offer := domain.QuestOffer{
		Player:     player,
		TemplateID: templateID,
		Definition: definition,
		Source:     source(kind),
		CreatedAt:  now,
		ExpiresAt:  now.Add(p.cfg.OfferTTL),
	}
	id, err := p.offers.InsertOffer(ctx, offer)
	if err != nil {
		return fmt.Errorf("insert offer: %w", err)
	}
	p.publishOffer(ctx, player, id, def, offer.Source, offer.ExpiresAt)

	return p.resetAndUpsert(ctx, &counters, count, threshold, tmin, tmax)
}

// produce picks a story quest (probability StoryShare) or a procedural template
// and returns what the offer/journal need. ok=false means nothing to offer.
// definition is the frozen instance JSONB for a procedural offer and nil for a
// story offer (which resolves through its static template_id at accept).
func (p *Pacer) produce(ctx context.Context, player domain.PlayerID, playerSector domain.SectorID) (templateID string, def quest.Def, definition []byte, ok bool, err error) {
	if p.rng.Float64() < p.cfg.StoryShare {
		templateID, def, ok = p.story.Pick(ctx, player)
		return templateID, def, nil, ok, nil
	}
	kind, def, ok, err := p.generator.Generate(ctx, playerSector)
	if err != nil {
		return "", quest.Def{}, nil, false, fmt.Errorf("generate offer: %w", err)
	}
	if !ok {
		return "", quest.Def{}, nil, false, nil
	}
	definition, err = json.Marshal(def)
	if err != nil {
		return "", quest.Def{}, nil, false, fmt.Errorf("marshal definition: %w", err)
	}
	return kind, def, definition, true, nil
}

// publishOffer emits the journal frame for a fresh offer. Best-effort: a
// marshal or publish error is logged, never failing the trigger (NFR-R: the
// offer already lives in the DB and the panel re-fetches it).
func (p *Pacer) publishOffer(ctx context.Context, player domain.PlayerID, id int64, def quest.Def, source string, expiresAt time.Time) {
	ev := quest.OfferEvent{
		OfferID:     fmt.Sprintf("proc:%d", id),
		Title:       def.Title,
		Desc:        offerDesc(def),
		Source:      source,
		ExpiresUnix: expiresAt.Unix(),
		RewardCash:  totalReward(def),
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		p.logger.ErrorContext(ctx, "marshal offer event", "err", err, "player", int64(player))
		return
	}
	if err := p.publisher.Publish(ctx, quest.OfferTopic(player), payload); err != nil {
		p.logger.ErrorContext(ctx, "publish offer event", "err", err, "player", int64(player))
	}
}

// fields returns pointers to the counter/threshold and the range bounds for the
// given trigger kind, so trigger's body is kind-agnostic.
func (p *Pacer) fields(c *domain.QuestCounters, kind triggerKind) (count, threshold *int, tmin, tmax int) {
	if kind == triggerJump {
		return &c.Jumps, &c.NextJumps, p.cfg.JumpsMin, p.cfg.JumpsMax
	}
	return &c.Docks, &c.NextDocks, p.cfg.DocksMin, p.cfg.DocksMax
}

// resetAndUpsert consumes a reached threshold: zero the counter, re-roll the
// threshold, persist. count/threshold point into c, so it dereferences c after
// the mutation.
func (p *Pacer) resetAndUpsert(ctx context.Context, c *domain.QuestCounters, count, threshold *int, tmin, tmax int) error {
	*count = 0
	*threshold = p.randRange(tmin, tmax)
	return p.upsert(ctx, *c)
}

func (p *Pacer) upsert(ctx context.Context, c domain.QuestCounters) error {
	if err := p.counters.UpsertCounters(ctx, c); err != nil {
		return fmt.Errorf("upsert counters: %w", err)
	}
	return nil
}

// randRange returns a uniform int in [min, max] (inclusive).
func (p *Pacer) randRange(minVal, maxVal int) int {
	if maxVal <= minVal {
		return minVal
	}
	return minVal + p.rng.Intn(maxVal-minVal+1)
}

func source(kind triggerKind) string {
	if kind == triggerJump {
		return "space"
	}
	return "dock"
}

// totalReward sums the per-step reward of a quest definition.
func totalReward(def quest.Def) int64 {
	var total int64
	for _, s := range def.Steps {
		total += s.RewardCash
	}
	return total
}

// offerDesc is the journal summary line: the first step's description when it
// has one, else the quest title.
func offerDesc(def quest.Def) string {
	if len(def.Steps) > 0 && def.Steps[0].Desc != "" {
		return def.Steps[0].Desc
	}
	return def.Title
}

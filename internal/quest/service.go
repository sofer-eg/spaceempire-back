package quest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
)

// procIDPrefix marks a quest id that refers to a generated offer instance
// (TASK-89). Accept resolves "proc:<offer_id>" through the offer store; every
// other id is a static catalogue quest resolved through Lookup.
const procIDPrefix = "proc:"

// ErrNotOfferable / ErrPrerequisiteNotMet gate the accept endpoint.
var (
	ErrNotOfferable       = errors.New("quest: not offerable")
	ErrPrerequisiteNotMet = errors.New("quest: prerequisite not completed")
)

// Store is the pool-backed reads + lazy-start the Service needs.
type Store interface {
	Get(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error)
	// Ensure starts a quest at step 0, optionally with a deadline (nil = none).
	// Idempotent (does nothing if a row already exists).
	Ensure(ctx context.Context, player domain.PlayerID, questID string, deadlineAt *time.Time) error
	// SetState persists the quest's state JSONB outside a step-advance tx (used
	// to record spawned-NPC links right after Accept). Phase 8.18.
	SetState(ctx context.Context, player domain.PlayerID, questID string, state []byte) error
	Abandon(ctx context.Context, player domain.PlayerID, questID string) error
	ListActive(ctx context.Context, limit int) ([]domain.QuestProgress, error)
	ListActiveByPlayer(ctx context.Context, player domain.PlayerID) ([]domain.QuestProgress, error)
	PlayerState(ctx context.Context, player domain.PlayerID) (Snapshot, error)
}

// Spawner injects (and removes) the NPCs a quest spawns (phase 8.18). Wired in
// app/ over the runtime spawn machinery; nil disables spawning so unit tests of
// non-spawn quests need no wiring. Despawn is best-effort (logs internally).
type Spawner interface {
	Spawn(ctx context.Context, spec QuestSpawn) ([]domain.ShipID, error)
	Despawn(ctx context.Context, ships []domain.ShipID)
}

// OfferStore reads the player's generated offers off the pool (TASK-89): the
// accept path fetches one offer by id (ownership-scoped), the offerable list
// reads all un-expired ones. Mutations (materialise + delete) run in the accept
// transaction via TxRepo. Satisfied by *PoolRepo.
type OfferStore interface {
	GetOffer(ctx context.Context, id int64, player domain.PlayerID) (domain.QuestOffer, bool, error)
	ListActiveOffersByPlayer(ctx context.Context, player domain.PlayerID, now time.Time) ([]domain.QuestOffer, error)
}

// TxRepo is the slice of mutations a step advance needs, bound to one tx so the
// reward and the step advance commit together (reward-exactly-once). It also
// carries the accept-time mutations (TASK-89): materialising a player_quests row
// and deleting the consumed offer in the same transaction, so accept is atomic
// (NFR-R / R-4: the TTL sweep never races a half-applied accept).
type TxRepo interface {
	// Lock re-reads the progress row FOR UPDATE so concurrent event/poll
	// advances serialise on it.
	Lock(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error)
	SetStep(ctx context.Context, player domain.PlayerID, questID string, step int) error
	SetState(ctx context.Context, player domain.PlayerID, questID string, state []byte) error
	Complete(ctx context.Context, player domain.PlayerID, questID string, finalStep int, at time.Time) error
	Fail(ctx context.Context, player domain.PlayerID, questID string, at time.Time) error
	AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error)
	// EnsureWithDefinition materialises a quest on accept — story (definition
	// nil → resolved via Lookup) and proc (frozen JSONB) share this one path.
	// It reports inserted=false when a row already existed (ON CONFLICT DO
	// NOTHING) so the caller gates the post-commit spawn on a real insert
	// (review C1). DeleteOffer consumes the offer that produced it (both in the
	// accept tx).
	EnsureWithDefinition(ctx context.Context, player domain.PlayerID, questID string, deadlineAt *time.Time, definition []byte) (bool, error)
	DeleteOffer(ctx context.Context, id int64, player domain.PlayerID) (bool, error)
}

// TxRunner runs fn inside a transaction with a TxRepo bound to it.
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, repo TxRepo) error) error
}

// Service starts/accepts/abandons quests, serves the active-quest read, and
// advances steps from polled state (poller) and discrete events (OnEvent),
// granting rewards and failing deadline-expired quests.
type Service struct {
	store   Store
	tx      TxRunner
	offers  OfferStore
	spawner Spawner
	clock   clock.Clock
	logger  *slog.Logger

	// brokenDefs deduplicates the "malformed definition" log so one zombie proc
	// row does not spam an Error on every poll tick / ship death (MR2 review).
	brokenDefs sync.Map
}

// New wires a Service. spawner may be nil (no quest-NPC spawning). A nil logger
// falls back to slog.Default.
func New(store Store, tx TxRunner, offers OfferStore, spawner Spawner, clk clock.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, tx: tx, offers: offers, spawner: spawner, clock: clk, logger: logger}
}

// ActiveView is one active/recent quest projected for GET /api/quests/active.
type ActiveView struct {
	QuestID      string
	Title        string
	Status       domain.QuestStatus
	StepIndex    int
	TotalSteps   int
	StepDesc     string
	StepReward   int64
	StepGoal     int64 // event-step target (0 for polled steps)
	StepProgress int64 // counter toward StepGoal
	Deadline     time.Time
	Done         bool
	Failed       bool
}

// ActiveList returns the player's active quests, lazy-starting the tutorial on
// first call.
func (s *Service) ActiveList(ctx context.Context, player domain.PlayerID) ([]*ActiveView, error) {
	if _, ok, err := s.store.Get(ctx, player, TutorialID); err != nil {
		return nil, err
	} else if !ok {
		if err := s.store.Ensure(ctx, player, TutorialID, nil); err != nil {
			return nil, err
		}
	}
	active, err := s.store.ListActiveByPlayer(ctx, player)
	if err != nil {
		return nil, err
	}
	out := make([]*ActiveView, 0, len(active))
	for _, prog := range active {
		if v := s.view(prog); v != nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// resolveDef resolves a progress row's quest definition. A procedural instance
// (proc:*, TASK-89) carries its frozen definition in prog.Definition (JSONB) and
// resolves from there; a static quest has none and resolves through the code
// registry (Lookup). A malformed definition resolves to ok=false so the engine
// degrades softly, exactly like an unknown id: view drops the quest and the
// advance paths no-op. Definition is immutable, so one resolve per progress row
// is reused across a re-locked cur (which carries the same bytes).
func (s *Service) resolveDef(prog domain.QuestProgress) (Def, bool) {
	if len(prog.Definition) > 0 {
		var def Def
		if err := json.Unmarshal(prog.Definition, &def); err != nil {
			s.logBrokenDefOnce("quest:"+prog.QuestID, err)
			return Def{}, false
		}
		return def, true
	}
	return Lookup(prog.QuestID)
}

// logBrokenDefOnce logs a malformed-definition Error the first time it sees a
// given key, then stays silent for it. Without this, OnShipDestroyed (which
// iterates every active quest on every ship death) and the poller would re-log
// the same broken proc row on every tick (MR2 review).
func (s *Service) logBrokenDefOnce(key string, err error) {
	if _, seen := s.brokenDefs.LoadOrStore(key, struct{}{}); !seen {
		s.logger.Error("quest resolve definition", "err", err, "key", key)
	}
}

// view projects progress + its definition into the API shape.
func (s *Service) view(p domain.QuestProgress) *ActiveView {
	def, ok := s.resolveDef(p)
	if !ok {
		return nil
	}
	v := &ActiveView{
		QuestID: def.ID, Title: def.Title, Status: p.Status,
		StepIndex: p.StepIndex, TotalSteps: len(def.Steps),
		Deadline: p.DeadlineAt,
		Done:     p.Status == domain.QuestCompleted,
		Failed:   p.Status == domain.QuestFailed,
	}
	if p.Status == domain.QuestActive && p.StepIndex < len(def.Steps) {
		step := def.Steps[p.StepIndex]
		v.StepDesc = step.Desc
		v.StepReward = step.RewardCash
		if step.EventDriven() || step.Kind == StepEscortSurvive {
			st := decodeState(p.State)
			v.StepGoal = step.Goal()
			v.StepProgress = st.Progress
			// A target-bound kill's goal is the number of spawned targets.
			if step.Kind == StepKill && step.TargetRole != "" {
				v.StepGoal = int64(len(st.Spawned[step.TargetRole]))
			}
		}
	}
	return v
}

// Accept starts a quest for the player. A "proc:<offer_id>" id materialises a
// generated offer (TASK-89); any other id is a static catalogue quest. The
// static path is unchanged (regression 8.17/8.18): the offerable list no longer
// surfaces the static catalogue, but a direct static accept still works.
func (s *Service) Accept(ctx context.Context, player domain.PlayerID, questID string) error {
	if offerID, ok := parseProcID(questID); ok {
		return s.acceptOffer(ctx, player, questID, offerID)
	}
	def, ok := Lookup(questID)
	if !ok || !def.Offerable {
		return ErrNotOfferable
	}
	if def.Prerequisite != "" {
		prog, ok, err := s.store.Get(ctx, player, def.Prerequisite)
		if err != nil {
			return err
		}
		if !ok || prog.Status != domain.QuestCompleted {
			return ErrPrerequisiteNotMet
		}
	}
	// Only the first accept spawns NPCs — re-accepting an active quest is a
	// no-op (Ensure is idempotent), so guard on whether a row already existed.
	_, existed, err := s.store.Get(ctx, player, questID)
	if err != nil {
		return err
	}
	var deadline *time.Time
	if def.Deadline > 0 {
		d := s.clock.Now().Add(def.Deadline)
		deadline = &d
	}
	if err := s.store.Ensure(ctx, player, questID, deadline); err != nil {
		return err
	}
	if !existed {
		return s.spawnFor(ctx, player, def)
	}
	return nil
}

// parseProcID extracts the offer id from a "proc:<n>" quest id. A malformed or
// non-proc id returns ok=false (the caller treats it as not offerable → 404).
func parseProcID(questID string) (int64, bool) {
	rest, ok := strings.CutPrefix(questID, procIDPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// acceptOffer materialises a generated offer into an active quest and consumes
// the offer, atomically (FR-07). A procedural offer freezes its definition into
// player_quests.definition (with the accepted id stamped so a re-generated kind
// never collides on the (player, quest_id) PK — MR3 review W3); a story offer
// resolves through its static template_id and materialises exactly like a direct
// static accept (definition NULL → resolved via Lookup thereafter). Ownership,
// absence and expiry all map to ErrNotOfferable (404; STRIDE spoofing).
//
// Both paths go through the one EnsureWithDefinition call so the spawn is gated
// on a real insert: the StaticStoryPicker can emit two live offers for the same
// spawn-bearing story quest (its eligibility check reads the player_quests row,
// not a live offer), and a concurrent double-accept of a single offer both reach
// here — in either case the second Ensure hits ON CONFLICT DO NOTHING
// (inserted=false) and must NOT spawn again, else batch #1 goes untracked (never
// counted toward the kill goal, never despawned) and its state is clobbered
// (review C1 / C-note).
func (s *Service) acceptOffer(ctx context.Context, player domain.PlayerID, questID string, offerID int64) error {
	offer, ok, err := s.offers.GetOffer(ctx, offerID, player)
	if err != nil {
		return fmt.Errorf("get offer: %w", err)
	}
	now := s.clock.Now()
	if !ok || !offer.ExpiresAt.After(now) {
		// Absent, foreign, or expired — indistinguishable to the caller by
		// design (no information disclosure), and re-accept is 404 by effect.
		return ErrNotOfferable
	}

	proc := len(offer.Definition) > 0
	var (
		def         Def
		activeID    string
		definition2 []byte // proc: frozen JSONB; story: nil (resolved via Lookup)
	)
	if proc {
		if err := json.Unmarshal(offer.Definition, &def); err != nil {
			return fmt.Errorf("unmarshal offer definition: %w", err)
		}
		activeID = questID // proc:<offer_id> — stable, collision-free PK
		def.ID = activeID
		if definition2, err = json.Marshal(def); err != nil {
			return fmt.Errorf("marshal accepted definition: %w", err)
		}
	} else {
		d, found := Lookup(offer.TemplateID)
		if !found {
			return ErrNotOfferable
		}
		def = d
		activeID = def.ID // static catalogue id, resolves via Lookup thereafter
		// W1 invariant: the story path mirrors the static Accept's Offerable
		// gate. A cheap guard, not a full prerequisite re-check — the
		// StaticStoryPicker already gated Prerequisite (completed is terminal)
		// when it created the offer, so re-verifying here would be redundant.
		if !def.Offerable {
			return ErrNotOfferable
		}
	}

	var deadline *time.Time
	if def.Deadline > 0 {
		d := now.Add(def.Deadline)
		deadline = &d
	}

	var inserted bool
	if err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		ins, err := repo.EnsureWithDefinition(ctx, player, activeID, deadline, definition2)
		if err != nil {
			return err
		}
		inserted = ins
		if _, err := repo.DeleteOffer(ctx, offerID, player); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return fmt.Errorf("accept offer: %w", err)
	}

	// Spawn only when this accept actually inserted the row (post-commit,
	// best-effort like the static Accept). A duplicate accept is a no-op by
	// effect — 204 without a second spawn. Procedural instances carry no spawns;
	// story offers can (X-Tension kill/escort quests).
	if inserted {
		return s.spawnFor(ctx, player, def)
	}
	return nil
}

// OfferView is one un-accepted offer projected for GET /api/quests/offerable
// (FR-10). OfferID is the "proc:<offer_id>" accept handle for both procedural
// and story offers.
type OfferView struct {
	OfferID     string
	Title       string
	Desc        string
	Source      string
	ExpiresUnix int64
	RewardCash  int64
}

// OfferableList returns the player's un-expired, un-accepted offers (FR-10 /
// AC-8): the offerable endpoint no longer serves the static catalogue, only
// these personal offers. An offer whose definition/template no longer resolves
// is skipped (soft degradation), so the list can be empty.
func (s *Service) OfferableList(ctx context.Context, player domain.PlayerID) ([]OfferView, error) {
	offers, err := s.offers.ListActiveOffersByPlayer(ctx, player, s.clock.Now())
	if err != nil {
		return nil, err
	}
	out := make([]OfferView, 0, len(offers))
	for _, o := range offers {
		def, ok := s.offerDef(o)
		if !ok {
			continue
		}
		out = append(out, OfferView{
			OfferID:     fmt.Sprintf("%s%d", procIDPrefix, o.ID),
			Title:       def.Title,
			Desc:        offerSummary(def),
			Source:      o.Source,
			ExpiresUnix: o.ExpiresAt.Unix(),
			RewardCash:  offerReward(def),
		})
	}
	return out, nil
}

// offerDef resolves an offer's definition: a procedural offer carries its frozen
// JSONB, a story offer resolves through Lookup(TemplateID). ok=false when the
// JSONB is malformed or the story template is unknown.
func (s *Service) offerDef(o domain.QuestOffer) (Def, bool) {
	if len(o.Definition) > 0 {
		var def Def
		if err := json.Unmarshal(o.Definition, &def); err != nil {
			s.logBrokenDefOnce(fmt.Sprintf("offer:%d", o.ID), err)
			return Def{}, false
		}
		return def, true
	}
	def, ok := Lookup(o.TemplateID)
	if !ok {
		// S2: an unknown story template silently drops from OfferableList; a
		// Debug line makes it observable, mirroring the malformed-JSONB path.
		s.logger.Debug("quest offer template not found", "offer", o.ID, "template", o.TemplateID)
	}
	return def, ok
}

// offerReward sums a definition's per-step rewards for the journal projection.
// (Mirrors the pacer's own summary; kept local so this MR does not touch the
// pacer package.)
func offerReward(def Def) int64 {
	var total int64
	for _, st := range def.Steps {
		total += st.RewardCash
	}
	return total
}

// offerSummary is the journal one-liner: the first step's description, else the
// title.
func offerSummary(def Def) string {
	if len(def.Steps) > 0 && def.Steps[0].Desc != "" {
		return def.Steps[0].Desc
	}
	return def.Title
}

// spawnFor resolves a quest's NPC spawns and records role→shipIDs in its state
// (phase 8.18). Best-effort: a spawn failure is logged and skipped (the quest
// stays active, possibly under-populated). No-op without a spawner / spawns.
func (s *Service) spawnFor(ctx context.Context, player domain.PlayerID, def Def) error {
	if s.spawner == nil || len(def.Spawns) == 0 {
		return nil
	}
	spawned := map[string][]int64{}
	for _, spec := range def.Spawns {
		ids, err := s.spawner.Spawn(ctx, spec)
		if err != nil {
			s.logger.ErrorContext(ctx, "quest spawn", "err", err, "quest", def.ID, "role", spec.Role)
			continue
		}
		for _, id := range ids {
			spawned[spec.Role] = append(spawned[spec.Role], int64(id))
		}
	}
	if len(spawned) == 0 {
		return nil
	}
	return s.store.SetState(ctx, player, def.ID, encodeState(questState{Spawned: spawned}))
}

// despawn removes the quest's still-living spawned NPCs. Best-effort.
func (s *Service) despawn(ctx context.Context, st questState) {
	if s.spawner == nil {
		return
	}
	if ids := st.allSpawned(); len(ids) > 0 {
		s.spawner.Despawn(ctx, ids)
	}
}

// Abandon drops an active quest (no reward) and despawns its NPCs.
func (s *Service) Abandon(ctx context.Context, player domain.PlayerID, questID string) error {
	prog, ok, err := s.store.Get(ctx, player, questID)
	if err != nil {
		return err
	}
	if err := s.store.Abandon(ctx, player, questID); err != nil {
		return err
	}
	if ok {
		s.despawn(ctx, decodeState(prog.State))
	}
	return nil
}

// OnEvent reconciles a discrete domain event (kill/deliver/trade) against the
// player's active quests, accumulating the counter on the matching current
// step and advancing (with reward) when the goal is reached. Each advance runs
// in its own tx with a FOR UPDATE lock so it serialises with the poller.
func (s *Service) OnEvent(ctx context.Context, ev Event) error {
	if ev.Player == 0 {
		return nil
	}
	active, err := s.store.ListActiveByPlayer(ctx, ev.Player)
	if err != nil {
		return err
	}
	for _, prog := range active {
		def, ok := s.resolveDef(prog)
		if !ok || prog.StepIndex >= len(def.Steps) {
			continue
		}
		step := def.Steps[prog.StepIndex]
		// Target-bound kills and escort are victim-scoped (OnShipDestroyed),
		// not killer-scoped — skip them here to avoid double-counting.
		if step.Kind == StepKill && step.TargetRole != "" {
			continue
		}
		if _, ok := step.MatchEvent(ev); !ok {
			continue
		}
		if err := s.applyEvent(ctx, prog, ev); err != nil {
			s.logger.ErrorContext(ctx, "quest on-event", "err", err,
				"player", int64(ev.Player), "quest", prog.QuestID)
		}
	}
	return nil
}

// OnShipDestroyed reconciles a ship death against every active quest by victim
// (any killer), the counterpart to killer-scoped OnEvent (phase 8.18). It
// drives the two victim-scoped step kinds: a target-bound kill credits the
// owning quest (so an NPC-stolen kill still counts), and an escort_survive
// step fails when its escortee dies. Called for every EntityKilledEvent.
func (s *Service) OnShipDestroyed(ctx context.Context, victim domain.EntityRef) error {
	if victim.Kind != domain.EntityKindShip {
		return nil
	}
	active, err := s.store.ListActive(ctx, CloserBatch)
	if err != nil {
		return err
	}
	for _, prog := range active {
		def, ok := s.resolveDef(prog)
		if !ok || prog.StepIndex >= len(def.Steps) {
			continue
		}
		step := def.Steps[prog.StepIndex]
		bound := step.TargetRole != "" && (step.Kind == StepKill || step.Kind == StepEscortSurvive)
		if !bound {
			continue
		}
		if !decodeState(prog.State).spawnedSet(step.TargetRole)[victim.ID] {
			continue
		}
		if err := s.applyDestroyed(ctx, prog, victim); err != nil {
			s.logger.ErrorContext(ctx, "quest on-destroyed", "err", err,
				"player", int64(prog.Player), "quest", prog.QuestID, "victim", victim.ID)
		}
	}
	return nil
}

// applyDestroyed applies a victim-scoped death to one quest in a FOR UPDATE tx:
// escort → fail; target-kill → progress toward "all targets dead", then
// reward + advance/complete. Despawns on any terminal transition.
func (s *Service) applyDestroyed(ctx context.Context, prog domain.QuestProgress, victim domain.EntityRef) error {
	def, ok := s.resolveDef(prog)
	if !ok {
		return nil
	}
	player, questID := prog.Player, prog.QuestID
	now := s.clock.Now()
	var despawnSt *questState
	err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		despawnSt = nil
		cur, ok, err := repo.Lock(ctx, player, questID)
		if err != nil || !ok || cur.Status != domain.QuestActive || cur.StepIndex >= len(def.Steps) {
			return err
		}
		step := def.Steps[cur.StepIndex]
		st := decodeState(cur.State)
		if !st.spawnedSet(step.TargetRole)[victim.ID] {
			return nil // already past this step / not our victim anymore
		}
		if step.Kind == StepEscortSurvive {
			despawnSt = &st
			return repo.Fail(ctx, player, questID, now)
		}
		// target-bound kill: count this victim toward "all spawned targets dead".
		goal := int64(len(st.Spawned[step.TargetRole]))
		st.Progress++
		if st.Progress < goal {
			return repo.SetState(ctx, player, questID, encodeState(st))
		}
		if step.RewardCash > 0 {
			if _, e := repo.AdjustCash(ctx, player, step.RewardCash); e != nil {
				return e
			}
		}
		if cur.StepIndex == len(def.Steps)-1 {
			despawnSt = &st
			return repo.Complete(ctx, player, questID, cur.StepIndex, now)
		}
		return advanceStep(ctx, repo, player, questID, cur.StepIndex, st.Spawned)
	})
	if err == nil && despawnSt != nil {
		s.despawn(ctx, *despawnSt)
	}
	return err
}

func (s *Service) applyEvent(ctx context.Context, prog domain.QuestProgress, ev Event) error {
	def, ok := s.resolveDef(prog)
	if !ok {
		return nil
	}
	player, questID := prog.Player, prog.QuestID
	now := s.clock.Now()
	var despawnSt *questState // set when the tx reaches a terminal transition
	err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		despawnSt = nil
		cur, ok, err := repo.Lock(ctx, player, questID)
		if err != nil || !ok || cur.Status != domain.QuestActive || cur.StepIndex >= len(def.Steps) {
			return err
		}
		step := def.Steps[cur.StepIndex]
		delta, ok := step.MatchEvent(ev)
		if !ok {
			return nil
		}
		st := decodeState(cur.State)
		st.Progress += delta
		if st.Progress < step.Goal() {
			return repo.SetState(ctx, player, questID, encodeState(st))
		}
		// Goal reached — grant reward and advance (or complete).
		if step.RewardCash > 0 {
			if _, e := repo.AdjustCash(ctx, player, step.RewardCash); e != nil {
				return e
			}
		}
		if cur.StepIndex == len(def.Steps)-1 {
			despawnSt = &st
			return repo.Complete(ctx, player, questID, cur.StepIndex, now)
		}
		return advanceStep(ctx, repo, player, questID, cur.StepIndex, st.Spawned)
	})
	if err == nil && despawnSt != nil {
		s.despawn(ctx, *despawnSt)
	}
	return err
}

// advanceStep moves past the current step. SetStep wipes the state to '{}', so
// any spawned-NPC links are re-written afterwards (later steps / despawn still
// reference them).
func advanceStep(ctx context.Context, repo TxRepo, player domain.PlayerID, questID string, fromStep int, spawned map[string][]int64) error {
	if err := repo.SetStep(ctx, player, questID, fromStep+1); err != nil {
		return err
	}
	if len(spawned) > 0 {
		return repo.SetState(ctx, player, questID, encodeState(questState{Spawned: spawned}))
	}
	return nil
}

// ProcessAll fails deadline-expired quests and advances every active quest
// whose current polled step the player's snapshot already satisfies. Called by
// the Closer. Event-driven steps are left to OnEvent.
func (s *Service) ProcessAll(ctx context.Context, limit int) error {
	active, err := s.store.ListActive(ctx, limit)
	if err != nil {
		return err
	}
	for _, prog := range active {
		if err := s.advance(ctx, prog); err != nil {
			s.logger.ErrorContext(ctx, "quest advance", "err", err,
				"player", int64(prog.Player), "quest", prog.QuestID)
		}
	}
	return nil
}

func (s *Service) advance(ctx context.Context, prog domain.QuestProgress) error {
	now := s.clock.Now()

	// Deadline first, BEFORE resolving the definition: an expired quest fails
	// regardless of step kind (and despawns its NPCs). Doing this ahead of
	// resolveDef means a proc instance whose definition no longer resolves
	// (corrupt JSONB) still fails on its deadline instead of lingering active
	// forever — the deadline path needs no definition (MR2 review).
	if !prog.DeadlineAt.IsZero() && now.After(prog.DeadlineAt) {
		var despawnSt *questState
		err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
			despawnSt = nil
			cur, ok, err := repo.Lock(ctx, prog.Player, prog.QuestID)
			if err != nil || !ok || cur.Status != domain.QuestActive {
				return err
			}
			s.logger.InfoContext(ctx, "quest failed (deadline)",
				"player", int64(prog.Player), "quest", prog.QuestID)
			st := decodeState(cur.State)
			despawnSt = &st
			return repo.Fail(ctx, prog.Player, prog.QuestID, now)
		})
		if err == nil && despawnSt != nil {
			s.despawn(ctx, *despawnSt)
		}
		return err
	}

	def, ok := s.resolveDef(prog)
	if !ok {
		return nil
	}

	// escort_survive advances by a survival timer (one tick per poll), not the
	// player snapshot — handle it before the polled loop.
	if prog.StepIndex < len(def.Steps) && def.Steps[prog.StepIndex].Kind == StepEscortSurvive {
		return s.advanceEscort(ctx, prog, def, now)
	}

	snap, err := s.store.PlayerState(ctx, prog.Player)
	if err != nil {
		return err
	}
	spawned := decodeState(prog.State).Spawned // constant across polled advances
	for prog.StepIndex < len(def.Steps) {
		step := def.Steps[prog.StepIndex]
		if step.EventDriven() || step.Kind == StepEscortSurvive || !step.Satisfied(snap) {
			break // event/escort steps advance elsewhere; unmet polled steps stop here
		}
		last := prog.StepIndex == len(def.Steps)-1
		stepIdx := prog.StepIndex
		if err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
			if step.RewardCash > 0 {
				if _, e := repo.AdjustCash(ctx, prog.Player, step.RewardCash); e != nil {
					return e
				}
			}
			if last {
				return repo.Complete(ctx, prog.Player, def.ID, stepIdx, now)
			}
			return advanceStep(ctx, repo, prog.Player, def.ID, stepIdx, spawned)
		}); err != nil {
			return err
		}
		s.logger.InfoContext(ctx, "quest step done",
			"player", int64(prog.Player), "quest", def.ID, "step", stepIdx, "reward", step.RewardCash, "completed", last)
		if last {
			s.despawn(ctx, decodeState(prog.State))
			break
		}
		prog.StepIndex++
	}
	return nil
}

// advanceEscort runs one survival tick for an escort_survive step: it bumps the
// survived-ticks counter and, when it reaches the goal, rewards + advances (or
// completes, despawning the escort). The escortee's death is handled by
// OnEvent (fail), not here.
func (s *Service) advanceEscort(ctx context.Context, prog domain.QuestProgress, def Def, now time.Time) error {
	step := def.Steps[prog.StepIndex]
	var completeSt *questState
	err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		completeSt = nil
		cur, ok, err := repo.Lock(ctx, prog.Player, prog.QuestID)
		if err != nil || !ok || cur.Status != domain.QuestActive || cur.StepIndex != prog.StepIndex {
			return err
		}
		st := decodeState(cur.State)
		st.Progress++
		if st.Progress < step.Goal() {
			return repo.SetState(ctx, prog.Player, def.ID, encodeState(st))
		}
		if step.RewardCash > 0 {
			if _, e := repo.AdjustCash(ctx, prog.Player, step.RewardCash); e != nil {
				return e
			}
		}
		if cur.StepIndex == len(def.Steps)-1 {
			completeSt = &st
			return repo.Complete(ctx, prog.Player, def.ID, cur.StepIndex, now)
		}
		return advanceStep(ctx, repo, prog.Player, def.ID, cur.StepIndex, st.Spawned)
	})
	if err == nil && completeSt != nil {
		s.despawn(ctx, *completeSt)
	}
	return err
}

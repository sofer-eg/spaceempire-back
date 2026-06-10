package quest

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
)

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

// TxRepo is the slice of mutations a step advance needs, bound to one tx so the
// reward and the step advance commit together (reward-exactly-once).
type TxRepo interface {
	// Lock re-reads the progress row FOR UPDATE so concurrent event/poll
	// advances serialise on it.
	Lock(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error)
	SetStep(ctx context.Context, player domain.PlayerID, questID string, step int) error
	SetState(ctx context.Context, player domain.PlayerID, questID string, state []byte) error
	Complete(ctx context.Context, player domain.PlayerID, questID string, finalStep int, at time.Time) error
	Fail(ctx context.Context, player domain.PlayerID, questID string, at time.Time) error
	AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error)
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
	spawner Spawner
	clock   clock.Clock
	logger  *slog.Logger
}

// New wires a Service. spawner may be nil (no quest-NPC spawning). A nil logger
// falls back to slog.Default.
func New(store Store, tx TxRunner, spawner Spawner, clk clock.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, tx: tx, spawner: spawner, clock: clk, logger: logger}
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

// view projects progress + its definition into the API shape.
func (s *Service) view(p domain.QuestProgress) *ActiveView {
	def, ok := Lookup(p.QuestID)
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

// Accept starts an offerable quest for the player after checking its
// prerequisite chain. Idempotent (re-accepting an active/done quest is a no-op).
func (s *Service) Accept(ctx context.Context, player domain.PlayerID, questID string) error {
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
		def, ok := Lookup(prog.QuestID)
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
		if err := s.applyEvent(ctx, prog.QuestID, ev); err != nil {
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
		def, ok := Lookup(prog.QuestID)
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
		if err := s.applyDestroyed(ctx, prog.Player, prog.QuestID, victim); err != nil {
			s.logger.ErrorContext(ctx, "quest on-destroyed", "err", err,
				"player", int64(prog.Player), "quest", prog.QuestID, "victim", victim.ID)
		}
	}
	return nil
}

// applyDestroyed applies a victim-scoped death to one quest in a FOR UPDATE tx:
// escort → fail; target-kill → progress toward "all targets dead", then
// reward + advance/complete. Despawns on any terminal transition.
func (s *Service) applyDestroyed(ctx context.Context, player domain.PlayerID, questID string, victim domain.EntityRef) error {
	def, ok := Lookup(questID)
	if !ok {
		return nil
	}
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

func (s *Service) applyEvent(ctx context.Context, questID string, ev Event) error {
	def, ok := Lookup(questID)
	if !ok {
		return nil
	}
	now := s.clock.Now()
	var despawnSt *questState // set when the tx reaches a terminal transition
	err := s.tx.Do(ctx, func(ctx context.Context, repo TxRepo) error {
		despawnSt = nil
		cur, ok, err := repo.Lock(ctx, ev.Player, questID)
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
			return repo.SetState(ctx, ev.Player, questID, encodeState(st))
		}
		// Goal reached — grant reward and advance (or complete).
		if step.RewardCash > 0 {
			if _, e := repo.AdjustCash(ctx, ev.Player, step.RewardCash); e != nil {
				return e
			}
		}
		if cur.StepIndex == len(def.Steps)-1 {
			despawnSt = &st
			return repo.Complete(ctx, ev.Player, questID, cur.StepIndex, now)
		}
		return advanceStep(ctx, repo, ev.Player, questID, cur.StepIndex, st.Spawned)
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
	def, ok := Lookup(prog.QuestID)
	if !ok {
		return nil
	}
	now := s.clock.Now()

	// Deadline first: an expired quest fails regardless of step kind (and
	// despawns its NPCs).
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

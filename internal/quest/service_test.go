package quest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/quest"
)

// memStore models the quest Store + TxRepo: progress per (player, quest), cash,
// and the polled snapshot fields the test sets directly. Keyed by player then
// quest id so chains (saga1+saga2) coexist for one player.
type memStore struct {
	progress map[domain.PlayerID]map[string]domain.QuestProgress
	cash     map[domain.PlayerID]int64
	snap     map[domain.PlayerID]quest.Snapshot
}

func newStore() *memStore {
	return &memStore{
		progress: map[domain.PlayerID]map[string]domain.QuestProgress{},
		cash:     map[domain.PlayerID]int64{},
		snap:     map[domain.PlayerID]quest.Snapshot{},
	}
}

func (m *memStore) put(p domain.QuestProgress) {
	if m.progress[p.Player] == nil {
		m.progress[p.Player] = map[string]domain.QuestProgress{}
	}
	m.progress[p.Player][p.QuestID] = p
}

func (m *memStore) Get(_ context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error) {
	p, ok := m.progress[player][questID]
	return p, ok, nil
}

func (m *memStore) Ensure(_ context.Context, player domain.PlayerID, questID string, deadlineAt *time.Time) error {
	if _, ok := m.progress[player][questID]; ok {
		return nil
	}
	p := domain.QuestProgress{Player: player, QuestID: questID, Status: domain.QuestActive}
	if deadlineAt != nil {
		p.DeadlineAt = *deadlineAt
	}
	m.put(p)
	return nil
}

func (m *memStore) Abandon(_ context.Context, player domain.PlayerID, questID string) error {
	if p, ok := m.progress[player][questID]; ok && p.Status == domain.QuestActive {
		p.Status = domain.QuestAbandoned
		m.put(p)
	}
	return nil
}

func (m *memStore) ListActive(_ context.Context, _ int) ([]domain.QuestProgress, error) {
	var out []domain.QuestProgress
	for _, byQuest := range m.progress {
		for _, p := range byQuest {
			if p.Status == domain.QuestActive {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func (m *memStore) ListActiveByPlayer(_ context.Context, player domain.PlayerID) ([]domain.QuestProgress, error) {
	var out []domain.QuestProgress
	for _, p := range m.progress[player] {
		if p.Status == domain.QuestActive {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) PlayerState(_ context.Context, player domain.PlayerID) (quest.Snapshot, error) {
	s := m.snap[player]
	s.Cash = m.cash[player]
	return s, nil
}

// --- TxRepo ---

func (m *memStore) Lock(_ context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error) {
	p, ok := m.progress[player][questID]
	return p, ok, nil
}

func (m *memStore) SetStep(_ context.Context, player domain.PlayerID, questID string, step int) error {
	p := m.progress[player][questID]
	p.StepIndex = step
	p.State = nil // reset counter
	m.put(p)
	return nil
}

func (m *memStore) SetState(_ context.Context, player domain.PlayerID, questID string, state []byte) error {
	p := m.progress[player][questID]
	p.State = state
	m.put(p)
	return nil
}

func (m *memStore) Complete(_ context.Context, player domain.PlayerID, questID string, finalStep int, at time.Time) error {
	p := m.progress[player][questID]
	p.StepIndex = finalStep
	p.Status = domain.QuestCompleted
	p.CompletedAt = at
	m.put(p)
	return nil
}

func (m *memStore) Fail(_ context.Context, player domain.PlayerID, questID string, at time.Time) error {
	p := m.progress[player][questID]
	if p.Status == domain.QuestActive {
		p.Status = domain.QuestFailed
		p.CompletedAt = at
		m.put(p)
	}
	return nil
}

func (m *memStore) AdjustCash(_ context.Context, p domain.PlayerID, delta int64) (int64, error) {
	m.cash[p] += delta
	return m.cash[p], nil
}

type runner struct{ store *memStore }

func (r runner) Do(ctx context.Context, fn func(ctx context.Context, repo quest.TxRepo) error) error {
	return fn(ctx, r.store)
}

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newService(store *memStore) (*quest.Service, *clock.FakeClock) {
	clk := clock.NewFakeClock(epoch)
	return quest.New(store, runner{store: store}, nil, clk, nil), clk
}

func (m *memStore) status(player domain.PlayerID, questID string) domain.QuestStatus {
	return m.progress[player][questID].Status
}

func TestUnit_Quest_ActiveLazyStarts(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, _ := newService(store)

	views, err := svc.ActiveList(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, quest.TutorialID, views[0].QuestID)
	assert.Equal(t, 3, views[0].TotalSteps)
	_, ok, _ := store.Get(context.Background(), 10, quest.TutorialID)
	assert.True(t, ok)
}

func TestUnit_Quest_AdvancesDockStepAndRewards(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, _ := newService(store)
	require.NoError(t, store.Ensure(context.Background(), 10, quest.TutorialID, nil))
	store.cash[10] = 10000
	store.snap[10] = quest.Snapshot{Docked: true} // step 0; no cargo → stops at step 1

	require.NoError(t, svc.ProcessAll(context.Background(), 100))
	assert.Equal(t, 1, store.progress[10][quest.TutorialID].StepIndex)
	assert.Equal(t, int64(10500), store.cash[10])

	require.NoError(t, svc.ProcessAll(context.Background(), 100))
	assert.Equal(t, 1, store.progress[10][quest.TutorialID].StepIndex)
	assert.Equal(t, int64(10500), store.cash[10], "reward not granted twice")
}

func TestUnit_Quest_CompletesAllStepsOneTick(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, _ := newService(store)
	require.NoError(t, store.Ensure(context.Background(), 10, quest.TutorialID, nil))
	store.snap[10] = quest.Snapshot{Docked: true, CargoUnits: 5}
	store.cash[10] = 15000

	require.NoError(t, svc.ProcessAll(context.Background(), 100))

	p := store.progress[10][quest.TutorialID]
	assert.Equal(t, domain.QuestCompleted, p.Status)
	assert.Equal(t, int64(21000), store.cash[10], "15000 + 500 + 500 + 5000")
}

// TestUnit_Quest_KillStepAccumulatesViaEvents drives the patrol quest's kill
// step (count 2) by two kill events: the first only bumps the counter, the
// second completes the quest and grants the reward exactly once.
func TestUnit_Quest_KillStepAccumulatesViaEvents(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, _ := newService(store)
	require.NoError(t, svc.Accept(context.Background(), 10, "patrol"))

	kill := quest.Event{Player: 10, Kind: quest.EventKill, Amount: 1}
	require.NoError(t, svc.OnEvent(context.Background(), kill))
	assert.Equal(t, domain.QuestActive, store.status(10, "patrol"), "1/2 kills — still active")
	assert.Equal(t, int64(0), store.cash[10])

	require.NoError(t, svc.OnEvent(context.Background(), kill))
	assert.Equal(t, domain.QuestCompleted, store.status(10, "patrol"))
	assert.Equal(t, int64(4000), store.cash[10], "reward once on completion")
}

// TestUnit_Quest_DeadlineFails accepts the deadline-bound patrol then advances
// the clock past the deadline; the poller flips it to failed (no reward).
func TestUnit_Quest_DeadlineFails(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, clk := newService(store)
	require.NoError(t, svc.Accept(context.Background(), 10, "patrol"))

	clk.Advance(25 * time.Hour) // past the 24h deadline
	require.NoError(t, svc.ProcessAll(context.Background(), 100))
	assert.Equal(t, domain.QuestFailed, store.status(10, "patrol"))
	assert.Equal(t, int64(0), store.cash[10], "no reward on failure")
}

// TestUnit_Quest_ChainPrerequisite — saga2 cannot be accepted until saga1 is
// completed.
func TestUnit_Quest_ChainPrerequisite(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, _ := newService(store)

	err := svc.Accept(context.Background(), 10, "saga2")
	require.ErrorIs(t, err, quest.ErrPrerequisiteNotMet)

	require.NoError(t, svc.Accept(context.Background(), 10, "saga1"))
	store.cash[10] = 20000
	require.NoError(t, svc.ProcessAll(context.Background(), 100)) // completes saga1
	require.Equal(t, domain.QuestCompleted, store.status(10, "saga1"))

	require.NoError(t, svc.Accept(context.Background(), 10, "saga2"))
	assert.Equal(t, domain.QuestActive, store.status(10, "saga2"))
}

func TestUnit_Quest_AbandonDropsQuest(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, _ := newService(store)
	require.NoError(t, svc.Accept(context.Background(), 10, "patrol"))

	require.NoError(t, svc.Abandon(context.Background(), 10, "patrol"))
	assert.Equal(t, domain.QuestAbandoned, store.status(10, "patrol"))

	// A kill event no longer advances an abandoned quest.
	require.NoError(t, svc.OnEvent(context.Background(), quest.Event{Player: 10, Kind: quest.EventKill, Amount: 1}))
	assert.Equal(t, domain.QuestAbandoned, store.status(10, "patrol"))
}

func TestUnit_Quest_AcceptRejectsNonOfferable(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc, _ := newService(store)
	require.ErrorIs(t, svc.Accept(context.Background(), 10, quest.TutorialID), quest.ErrNotOfferable)
	require.ErrorIs(t, svc.Accept(context.Background(), 10, "nope"), quest.ErrNotOfferable)
}

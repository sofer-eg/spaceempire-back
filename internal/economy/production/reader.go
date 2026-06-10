package production

import (
	"context"
	"time"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// CycleInfo describes a station's production cycle for the UI. Produces is
// false when the station's type has no recipe (the other fields are then
// zero and the caller should omit any production block).
type CycleInfo struct {
	Produces    bool
	InProgress  bool
	NextCycleAt time.Time
	CycleTime   time.Duration
}

// StationStore is the single read a Reader needs — kept minimal (ISP) so
// the pool-bound persistence/stations repository satisfies it directly.
type StationStore interface {
	GetStation(ctx context.Context, id domain.StationID) (domain.Station, error)
}

// Reader answers "what is this station's production cycle?" for read-only
// callers (the market endpoint). It is deliberately separate from the
// tick Service so a UI lookup never touches the cycle write path.
type Reader struct {
	bal   *balance.Balance
	store StationStore
}

func NewReader(bal *balance.Balance, store StationStore) *Reader {
	return &Reader{bal: bal, store: store}
}

// StationCycle reads the station's persisted cycle state and joins it with
// the recipe's cycle length from balance. A station type without a recipe
// yields CycleInfo{Produces: false}.
func (r *Reader) StationCycle(ctx context.Context, id domain.StationID) (CycleInfo, error) {
	st, err := r.store.GetStation(ctx, id)
	if err != nil {
		return CycleInfo{}, err
	}
	recipe, ok := r.bal.Recipe(st.Type)
	if !ok {
		return CycleInfo{}, nil
	}
	return CycleInfo{
		Produces:    true,
		InProgress:  st.InProgress,
		NextCycleAt: st.NextCycleAt,
		CycleTime:   recipe.CycleTime,
	}, nil
}

package sector

import (
	"context"
	"errors"
	"sync"

	"spaceempire/back/internal/domain"
)

// subscriptionBufferSize is the per-subscriber Patch channel capacity. A
// stalled client whose buffer is full causes broadcasts to drop, not block
// the tick loop.
const subscriptionBufferSize = 16

// ErrSubscribeCanceled is returned by Subscribe when ctx is canceled before
// the worker handled the subscribe command.
var ErrSubscribeCanceled = errors.New("sector: subscribe canceled")

// Subscription is an open per-player stream of Patch updates for a single
// sector. Patch is buffered; if the consumer falls behind, the worker drops
// new patches for this subscriber rather than blocking the tick loop. Always
// call the Unsubscribe func returned from Subscribe to release it.
//
// Center / Radius drive the AOI filter: every tick the worker refreshes
// Center to track the player's own ship in this sector, queries the spatial
// grid for ships within Radius, and only diffs that visible subset against
// lastSent. Both fields are written exclusively by the tick goroutine.
type Subscription struct {
	SectorID domain.SectorID
	PlayerID domain.PlayerID
	Patch    <-chan Patch

	Center domain.Vec2
	Radius float64

	// internal — used by the worker
	id                uint64
	patchOut          chan Patch
	lastSent          map[domain.ShipID]domain.Ship
	lastSentMissile   map[domain.MissileID]domain.Missile
	lastSentDrone     map[domain.DroneID]domain.Drone
	lastSentTorpedo   map[domain.TorpedoID]domain.Torpedo
	lastSentContainer map[domain.ContainerID]domain.Container
	lastSentAsteroid  map[domain.AsteroidID]domain.Asteroid
	// lastSentStatics is the set of static refs the subscriber currently has
	// (phase 10.20 L2). Seeded at subscribe with every live static (the welcome
	// StaticsMessage sends them all); the per-tick big-radar diff then adds /
	// removes statics as the player moves.
	lastSentStatics map[domain.EntityRef]struct{}
}

type subscribeCommand struct {
	sectorID domain.SectorID
	playerID domain.PlayerID
	reply    chan<- *Subscription
}

func (c subscribeCommand) apply(w *Worker, s *sectorState) {
	id := w.nextSubID()
	out := make(chan Patch, subscriptionBufferSize)
	sub := &Subscription{
		SectorID: c.sectorID,
		PlayerID: c.playerID,
		Patch:    out,
		Radius:   w.cfg.AOIRadius,
		id:       id,
		patchOut: out,
		// The welcome StaticsMessage delivers every static, so seed the seen-set
		// with all of them; the first big-radar diff trims to the window (10.20).
		lastSentStatics: s.liveStaticRefs(),
	}
	s.subs[sub.id] = sub
	select {
	case c.reply <- sub:
	default:
	}
}

type unsubscribeCommand struct {
	id uint64
}

func (c unsubscribeCommand) apply(_ *Worker, s *sectorState) {
	sub, ok := s.subs[c.id]
	if !ok {
		return
	}
	delete(s.subs, c.id)
	close(sub.patchOut)
}

// Subscribe registers a new patch listener on the given sector for playerID.
// The returned subscription receives Patch messages on every tick that
// produces a non-empty diff; the first patch always contains the full
// current ship set in Added so a fresh client can render immediately.
//
// The returned unsubscribe func must be called when the consumer is done
// (e.g. on WS disconnect). It signals the worker to close the channel; it
// is safe to call from any goroutine and safe to call twice.
func (w *Worker) Subscribe(ctx context.Context, sectorID domain.SectorID, playerID domain.PlayerID) (*Subscription, func(), error) {
	reply := make(chan *Subscription, 1)
	if err := w.Send(sectorID, subscribeCommand{
		sectorID: sectorID,
		playerID: playerID,
		reply:    reply,
	}); err != nil {
		return nil, nil, err
	}
	select {
	case sub := <-reply:
		var once sync.Once
		unsub := func() {
			once.Do(func() {
				_ = w.Send(sectorID, unsubscribeCommand{id: sub.id})
			})
		}
		return sub, unsub, nil
	case <-ctx.Done():
		return nil, nil, ErrSubscribeCanceled
	}
}

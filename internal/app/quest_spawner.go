package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	aistaterepo "spaceempire/back/internal/persistence/aistate"
	shipsrepo "spaceempire/back/internal/persistence/ships"
	"spaceempire/back/internal/quest"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// questSpawner satisfies the quest spawn port.
var _ quest.Spawner = (*questSpawner)(nil)

// questNPCAckTimeout bounds the wait for a worker to mirror / remove a quest
// NPC. The DB row is already persisted (spawn) or the despawn is best-effort,
// so a missed ack is logged, not fatal.
const questNPCAckTimeout = time.Second

// questSpawner implements quest.Spawner over the 9.5 runtime spawn machinery
// (phase 8.18): a quest NPC is a system-owned race ship driven by the race
// controller (buildWarship + ships.Create + ai_state + AddShipCommand), the
// same path the invasion spawner uses. Despawn finds each ship's live sector
// and sends RemoveShipCommand. No npc_ships row (like invasion ships); they
// reload as race NPCs on restart and the quest's state keeps referencing them.
type questSpawner struct {
	ships    *shipsrepo.Repository
	aiState  *aistaterepo.Repository
	pool     *sector.Pool
	topology *world.Topology
	classes  *balance.ShipClasses
	npc      domain.PlayerID
	shipCfg  ShipSpawnerConfig
	logger   *slog.Logger
}

// Spawn injects spec.Count race ships into spec.Sector. FromGate spawns at a
// gate exit (advancing inward); otherwise at the sector centre. Returns the
// created ship ids (in order), or a partial list with an error.
func (q *questSpawner) Spawn(ctx context.Context, spec quest.QuestSpawn) ([]domain.ShipID, error) {
	classes := combatClassesForRace(q.classes.AllShipClasses(), spec.Race, 3, 4, 5)
	if len(classes) == 0 {
		return nil, fmt.Errorf("quest spawn: no ship class for race %d", spec.Race)
	}
	base := domain.Vec2{}   // sector centre
	anchor := domain.Vec2{} // patrol anchor (centre = advance inward)
	if spec.FromGate {
		if exit, ok := gateExitInto(q.topology, spec.Sector); ok {
			base = exit
		}
	} else {
		anchor = base
	}

	var ids []domain.ShipID
	for i := 0; i < spec.Count; i++ {
		class := classes[i%len(classes)]
		ship := buildWarship(q.npc, spec.Race, class, q.shipCfg)
		ship.SectorID = spec.Sector
		ship.Pos = ringOffset(base, i, spec.Count, invasionSpawnRing)
		id, err := q.ships.Create(ctx, ship)
		if err != nil {
			return ids, fmt.Errorf("create quest npc: %w", err)
		}
		ship.ID = id

		stateJSON, err := race.NewInitialState(int(spec.Race), anchor)
		if err != nil {
			return ids, fmt.Errorf("quest npc state: %w", err)
		}
		if err := q.aiState.BatchUpsert(ctx, []domain.AIState{{
			ShipID:         id,
			SectorID:       spec.Sector,
			ControllerKind: race.Kind,
			StateJSON:      stateJSON,
		}}); err != nil {
			return ids, fmt.Errorf("quest npc ai state: %w", err)
		}
		if err := q.send(spec.Sector, sector.AddShipCommand{
			Ship:           ship,
			ControllerKind: race.Kind,
			StateJSON:      stateJSON,
		}); err != nil {
			return ids, fmt.Errorf("send quest npc: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Despawn removes each still-living quest NPC from its current sector. A ship
// the pool no longer knows (already killed) is skipped.
func (q *questSpawner) Despawn(ctx context.Context, ids []domain.ShipID) {
	for _, id := range ids {
		sectorID, ok := q.pool.LookupShipSector(id)
		if !ok {
			continue
		}
		if err := q.send(sectorID, sector.RemoveShipCommand{ShipID: id}); err != nil {
			q.logger.ErrorContext(ctx, "quest despawn", "err", err, "ship", int64(id))
		}
	}
}

// send dispatches a reply-bearing command to a sector and waits briefly for the
// worker to apply it. The cmd's Reply is filled in here.
func (q *questSpawner) send(sectorID domain.SectorID, cmd sector.Command) error {
	reply := make(chan sector.CmdResult, 1)
	switch c := cmd.(type) {
	case sector.AddShipCommand:
		c.Reply = reply
		cmd = c
	case sector.RemoveShipCommand:
		c.Reply = reply
		cmd = c
	}
	if err := q.pool.Send(sectorID, cmd); err != nil {
		return err
	}
	select {
	case res := <-reply:
		return res.Err
	case <-time.After(questNPCAckTimeout):
		return nil // best-effort: the row is persisted / removal is idempotent
	}
}

// gateExitInto returns the exit position on the side of a gate that lands in
// the given sector (where a from-gate spawn breaks through), if any.
func gateExitInto(topology *world.Topology, sectorID domain.SectorID) (domain.Vec2, bool) {
	for _, g := range topology.Gates() {
		if g.SectorA == sectorID {
			return g.PosA, true
		}
		if g.SectorB == sectorID {
			return g.PosB, true
		}
	}
	return domain.Vec2{}, false
}

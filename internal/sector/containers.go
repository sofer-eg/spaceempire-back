package sector

import (
	"context"
	"sort"
	"time"

	"spaceempire/back/internal/domain"
)

// tickContainers sweeps loot containers past their TTL: it removes them
// from RAM and deletes them (with their cargo) from the DB immediately,
// mirroring the drone self-destruct path. No-op without a container repo.
func (w *Worker) tickContainers(ctx context.Context, s *sectorState, now time.Time) {
	var expired []domain.ContainerID
	for id, c := range s.containers {
		if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		s.removeContainer(id)
		if w.containerRepo != nil {
			if err := w.containerRepo.Delete(ctx, id); err != nil {
				w.logger.ErrorContext(ctx, "container expiry delete failed",
					"err", err, "container", int64(id), "sector", int64(s.sectorID))
			}
		}
	}
}

// snapshotContainers returns a sorted-by-ID slice of value-type
// containers for the published Snapshot. Container has no slice fields,
// so a plain value copy satisfies the worker→subscriber isolation
// contract.
func snapshotContainers(src map[domain.ContainerID]*domain.Container) []domain.Container {
	if len(src) == 0 {
		return nil
	}
	out := make([]domain.Container, 0, len(src))
	for _, c := range src {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// containersInRadius returns the subset of containers whose Pos lies
// within radius of center. radius<=0 disables the filter.
func containersInRadius(src map[domain.ContainerID]*domain.Container, center domain.Vec2, radius float64) map[domain.ContainerID]domain.Container {
	if len(src) == 0 {
		return nil
	}
	out := make(map[domain.ContainerID]domain.Container, len(src))
	if radius <= 0 {
		for id, c := range src {
			out[id] = *c
		}
		return out
	}
	r2 := radius * radius
	for id, c := range src {
		if pointInRadius2(c.Pos, center, r2) {
			out[id] = *c
		}
	}
	return out
}

// diffContainers produces the per-tick container delta vs the
// subscriber's previously-seen set. Containers are immutable once
// created, so there is no "updated" bucket — only added and removed.
func diffContainers(prev, curr map[domain.ContainerID]domain.Container) (added []domain.Container, removed []domain.ContainerID) {
	for id, c := range curr {
		if _, existed := prev[id]; !existed {
			added = append(added, c)
		}
	}
	for id := range prev {
		if _, still := curr[id]; !still {
			removed = append(removed, id)
		}
	}
	return added, removed
}

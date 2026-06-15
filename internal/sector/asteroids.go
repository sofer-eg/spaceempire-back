package sector

import "spaceempire/back/internal/domain"

// asteroidsInRadius returns the subset of asteroids whose Pos lies within
// radius of center. radius<=0 disables the filter (returns all). Output is a
// value-type map, deep-safe because Asteroid has no pointer fields. Mirrors
// containersInRadius — asteroids are static, so the membership test is the
// same point-in-circle check.
func asteroidsInRadius(src map[domain.AsteroidID]*domain.Asteroid, center domain.Vec2, radius float64) map[domain.AsteroidID]domain.Asteroid {
	if len(src) == 0 {
		return nil
	}
	out := make(map[domain.AsteroidID]domain.Asteroid, len(src))
	if radius <= 0 {
		for id, a := range src {
			out[id] = *a
		}
		return out
	}
	r2 := radius * radius
	for id, a := range src {
		if pointInRadius2(a.Pos, center, r2) {
			out[id] = *a
		}
	}
	return out
}

// diffAsteroids produces the per-tick asteroid delta vs the subscriber's
// previously-seen set. Unlike containers (immutable), an asteroid's Mass
// shrinks as it is mined, so a body present in both frames with a changed
// Mass goes to updated. Pos/OreType never change, so a plain `==` on the
// value type detects the mass change. Depleted asteroids leave the live set
// and surface in removed.
func diffAsteroids(prev, curr map[domain.AsteroidID]domain.Asteroid) (added, updated []domain.Asteroid, removed []domain.AsteroidID) {
	for id, a := range curr {
		pv, existed := prev[id]
		switch {
		case !existed:
			added = append(added, a)
		case pv != a:
			updated = append(updated, a)
		}
	}
	for id := range prev {
		if _, still := curr[id]; !still {
			removed = append(removed, id)
		}
	}
	return added, updated, removed
}

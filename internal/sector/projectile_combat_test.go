package sector

import (
	"context"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

// stubTorpedoRepo is a minimal in-package TorpedoRepo for the white-box shoot-down
// tests: it only needs to count Delete calls (Create/BatchUpdate are unused on
// these paths). The package-external fakeTorpedoRepo lives in sector_test and is
// not visible here.
type stubTorpedoRepo struct{ deletes int }

func (r *stubTorpedoRepo) Create(context.Context, domain.Torpedo) (domain.TorpedoID, error) {
	return 0, nil
}
func (r *stubTorpedoRepo) BatchUpdate(context.Context, []domain.Torpedo) error { return nil }
func (r *stubTorpedoRepo) Delete(context.Context, domain.TorpedoID) error {
	r.deletes++
	return nil
}

const projSector = domain.SectorID(1)

// laserDefender is a ship rigged to fire its laser at ref every tick.
func laserDefender(id, playerID int64, pos domain.Vec2, dmg int, ref domain.EntityRef) domain.Ship {
	t := ref
	return domain.Ship{
		ID: domain.ShipID(id), PlayerID: domain.PlayerID(playerID), SectorID: projSector, Pos: pos,
		Direction: domain.Vec2{X: 1, Y: 0}, HP: 200, MaxHP: 200,
		Energy: 1000, MaxEnergy: 1000,
		LaserDamage: dmg, LaserRange: 100000, LaserEnergyCost: 0,
		AttackTarget: &t,
	}
}

// inFlightTorpedo builds a class-2-profile torpedo owned by ownerID, homing at
// target, with the given HP. Pos/Direction/Speed give it a real homing step.
func inFlightTorpedo(id domain.TorpedoID, ownerID, playerID int64, pos domain.Vec2, target domain.EntityRef, targetPos domain.Vec2, hp int, now time.Time) domain.Torpedo {
	return domain.Torpedo{
		ID: id, SectorID: projSector, OwnerShipID: domain.ShipID(ownerID), PlayerID: domain.PlayerID(playerID),
		Pos: pos, Direction: domain.Vec2{X: 1, Y: 0},
		Target: target, LastTargetPos: targetPos, Class: 2,
		Damage: 150, Speed: 30, Accel: 15, TurnRate: math.Pi / 3,
		HitRadius: 14, SplashRadius: 40, HP: hp,
		ExpiresAt: now.Add(30 * time.Second),
	}
}

func projTickWorker(repo TorpedoRepo) *Worker {
	return &Worker{logger: slog.New(slog.DiscardHandler), torpedoRepo: repo}
}

// TestUnit_Torpedo_LaserShootsDown is the AC-7 / AC #1+#2 white-box: a laser
// whose AttackTarget is a torpedo routes damage into the torpedo's TakeDamage
// (so EntityRef{torpedo} resolves to s.torpedos[id]), and a hit that reaches the
// torpedo's HP destroys it BEFORE it reaches its own target — reaped by
// tickTorpedos with impact(killed), persisted Delete, and NO splash to the ship
// and bystander sitting inside the (would-be) blast radius.
func TestUnit_Torpedo_LaserShootsDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	owner := domain.Ship{ID: 1, PlayerID: 100, SectorID: projSector, Pos: domain.Vec2{X: -500, Y: 0}, HP: 200, MaxHP: 200}
	// The torpedo's intended victim, far away so the torpedo could never detonate
	// on it within the test — any damage to it would have to be splash.
	victim := domain.Ship{ID: 3, PlayerID: 300, SectorID: projSector, Pos: domain.Vec2{X: 1_000_000, Y: 0}, HP: 1000, MaxHP: 1000}
	// A bystander parked next to the torpedo, well inside its 40-unit SplashRadius:
	// proves a shoot-down deals no area damage.
	bystander := domain.Ship{ID: 4, PlayerID: 400, SectorID: projSector, Pos: domain.Vec2{X: 5, Y: 5}, HP: 100, MaxHP: 100}

	torpRef := domain.EntityRef{Kind: domain.EntityKindTorpedo, ID: 7}
	// Defender's laser one-shots the 40-HP torpedo (dmg 100 >= HP 40).
	defender := laserDefender(2, 200, domain.Vec2{X: 10, Y: 0}, 100, torpRef)

	torp := inFlightTorpedo(7, 1, 100, domain.Vec2{X: 0, Y: 0},
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 3}, victim.Pos, 40, now)

	s := newSectorState(projSector, []domain.Ship{owner, defender, victim, bystander}, nil,
		[]domain.Torpedo{torp}, nil, nil, domain.SectorStatics{}, now)
	repo := &stubTorpedoRepo{}
	w := projTickWorker(repo)

	// fireLasers (runs before tickTorpedos in a real tick) drives the torpedo's
	// HP to 0 via the projectile extension point.
	w.fireLasers(ctx, s)
	require.LessOrEqual(t, s.torpedos[7].HP, 0, "laser routed damage into the torpedo and dropped its HP to 0")
	require.Nil(t, s.ships[2].AttackTarget, "a killing beam drops the engagement")

	// tickTorpedos reaps the shot-down torpedo with impact(killed), no splash.
	w.tickTorpedos(ctx, s, 1, now)

	_, alive := s.torpedos[7]
	require.False(t, alive, "shot-down torpedo removed from the live set")
	require.Equal(t, 1, repo.deletes, "shoot-down persists the removal (Delete)")

	require.Len(t, s.torpedoImpacts, 1, "exactly one torpedo impact this tick")
	imp := s.torpedoImpacts[0]
	require.True(t, imp.Killed, "impact is a Killed (shot-down) outcome")
	require.False(t, imp.Hit, "a shoot-down is not a detonation")
	require.False(t, imp.Expired, "a shoot-down is not a TTL/owner expiry")
	require.Zero(t, imp.SplashRadius, "a shoot-down carries no splash radius")

	require.Equal(t, 100, s.ships[4].HP, "no splash: the bystander inside the blast radius is unharmed")
	require.Equal(t, 1000, s.ships[3].HP, "the torpedo's own target took no damage — it never reached it")
}

// TestUnit_Torpedo_LaserChipDamageKeepsHoming is the AC #3 counter-case: a laser
// that deals LESS than the torpedo's HP only chips it — the torpedo stays alive,
// its HP is lowered, and it keeps homing toward its target on the same tick
// (no Killed impact).
func TestUnit_Torpedo_LaserChipDamageKeepsHoming(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	owner := domain.Ship{ID: 1, PlayerID: 100, SectorID: projSector, Pos: domain.Vec2{X: -500, Y: 0}, HP: 200, MaxHP: 200}
	target := domain.Ship{ID: 3, PlayerID: 300, SectorID: projSector, Pos: domain.Vec2{X: 5000, Y: 0}, HP: 1000, MaxHP: 1000}

	torpRef := domain.EntityRef{Kind: domain.EntityKindTorpedo, ID: 7}
	// Laser deals 10 < the torpedo's 40 HP — a chip, not a kill.
	defender := laserDefender(2, 200, domain.Vec2{X: 10, Y: 0}, 10, torpRef)

	start := domain.Vec2{X: 0, Y: 0}
	torp := inFlightTorpedo(7, 1, 100, start,
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 3}, target.Pos, 40, now)

	s := newSectorState(projSector, []domain.Ship{owner, defender, target}, nil,
		[]domain.Torpedo{torp}, nil, nil, domain.SectorStatics{}, now)
	w := projTickWorker(&stubTorpedoRepo{})

	w.fireLasers(ctx, s)
	require.Equal(t, 30, s.torpedos[7].HP, "chip damage lowers HP (40-10) but does not kill")
	require.NotNil(t, s.ships[2].AttackTarget, "the defender stays engaged on a non-killing beam")

	w.tickTorpedos(ctx, s, 1, now)

	live, alive := s.torpedos[7]
	require.True(t, alive, "the chipped torpedo survives and stays in flight")
	require.Equal(t, 30, live.HP, "tick deals no further damage to the torpedo itself")
	require.NotEqual(t, start, live.Pos, "the surviving torpedo keeps homing (position advanced)")
	require.True(t, s.torpedosDirty[7], "the surviving torpedo is dirty for the persistence batch")
	require.Empty(t, s.torpedoImpacts, "a surviving torpedo emits no impact")
}

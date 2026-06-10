# Spec: Drones (port of SP `TO_Drones`)

Phase 4.4. Source: `starwind/sql/db.sql` — `TO_Drones` (model=1 branch),
`DronesSetOrders`, `DroneTargeting_GetCurrentTargetParameters`.

## 1. Scope vs the original SP

The original `TO_Drones` handles six drone *models* (1 combat drone, 2
torpedo, 3 mine, 4 flare, 5 nav-sat, 6 hyper-gen), an eight-state
`Orders` machine (1/2/3 attack assigned target, 4/5/6 auto-acquire
nearest hostile, 7 return-to-launcher, 8 self-destruct), evade
manoeuvres, and a ~200-line per-tick movement integrator with
deceleration planning.

Phase 4.4 ports **only the combat drone (model=1)** with a deliberately
reduced behaviour set:

- **Target is explicitly assigned at launch** (`launch-drone` carries a
  `targetRef`). This corresponds to the SP's `Orders=1` (attack assigned
  target). **Auto-acquisition (`Orders=4/5/6`) is NOT ported** because it
  depends on `tmp_tbl_ship_targets.hostile`, which is computed by
  `TO_HostilityPrecount` from race/clan relations — that is phase 6.2,
  not yet implemented. **TODO(6.2):** once relations land, add a
  "nearest hostile in detection radius" acquisition step and replace the
  interim predicate (see §4).
- **Movement** ports the same physics family already implemented for
  missiles (`combat.TickMissile`, a port of `TO_Missiles`): turn toward
  target by `TurnRate·dt`, accelerate along heading, strafe-compensate
  the perpendicular drift, apply proportional friction, clamp to
  `Speed`, integrate `pos += vel·dt`. The drone-specific addition is
  **standoff braking**: within `StandoffRange` the drone decelerates so
  it holds station near the target and fires repeatedly, instead of
  flying through like a one-shot missile (SP `parrots_to_achieve`
  braking). The SP's `turns_to_target`/`turns_to_stop` deceleration
  *planning* is approximated by proportional braking — same fidelity
  trade-off `TickMissile` made against `TO_Missiles` (see
  `missiles.md` §1).
- **No torpedo/mine/flare/nav-sat/hyper-gen models.**
- **No evade.**

## 2. Cargo cost

Each launched drone consumes **one `Combat Drone` cargo unit** (goods
type id `51`, seeded by migration `0018_drones.sql`, `space=2`, chosen
above the missile id `50`). `launch-drone {shipID, count}` consumes
`count` units up front; `recall-drones {shipID}` returns **one unit per
still-alive drone** owned by that ship to its cargo.

The HTTP handler is the orchestrator between Postgres cargo and the
in-RAM sector worker, mirroring `launch-missile`:

1. validate request,
2. `Consume(shipRef, DroneGoodsType, count)`,
3. send `LaunchDroneCommand` and wait for ack (which returns the number
   actually spawned),
4. on worker rejection — `Refund` the full `count`.

Recall is the reverse: the worker removes the drones and replies with
the recalled count; the handler `Refund`s that many units.

## 3. Persistence (immediate, unlike missiles)

Drones are **persistent state** (acceptance criterion: "при рестарте
сервера дроны восстанавливаются"). Unlike missiles (reconstructable, RAM
only) the drone lifecycle writes to the `drones` table:

- **immediate INSERT** on launch (one row per drone),
- **immediate DELETE** on death / expire / recall,
- **periodic BatchUpdate** of mutable fields (pos, vel, direction, hp,
  target, expires_at) on the worker's dirty-set / snapshot interval,
  mirroring ships,
- **cold-start LoadAll** rebuilds the live set per sector at startup.

The `drones` table id is the authoritative `DroneID` (DB-assigned at
INSERT), not a per-worker counter — so it survives restarts.

## 4. Targeting & hostility (interim)

`launch-drone` carries `targetRef` (an `EntityKindShip`). Every tick the
sector resolves the target:

- target ship alive & in this sector → drone steers to it, brakes at
  `StandoffRange`, fires when in `FireRange` and the target is in front;
- target dead / left sector → drone steers back toward its **owner
  ship** (loiter) until TTL;
- owner ship dead / gone → drone **self-destructs** immediately.

**INTERIM / TODO(6.2):** there is currently no hostility check at all —
a drone shoots exactly the ship it was launched at, regardless of
relations, and there is no auto-acquisition of the "nearest hostile".
When phase 6.2 (relations/`TO_HostilityPrecount`) lands:
- add a `isHostile(owner, candidate) bool` predicate,
- add the SP `Orders=4` auto-acquire step (nearest hostile ship within a
  detection radius around the drone/launcher),
- gate firing on `isHostile`.
This is isolated to `combat`/`sector` drone code; the persistence and
transport layers do not change.

## 5. Lifecycle outcomes (per tick)

`combat.TickDrone` returns one of:

- `DroneKeep` — still alive; the worker keeps it, marks dirty, may fire.
- `DroneExpired` — TTL elapsed **or** owner gone; the worker removes the
  drone (immediate DELETE) and emits a `DroneImpact{Expired:true}`.

Firing is a separate boolean the worker turns into
`combat.ApplyDamage(targetShip, spec.Damage)` + a `DroneImpact` carrying
the dealt damage and `Killed` flag, exactly like missiles.

## 6. Default spec (phase 4.4, single class)

`combat.DefaultDroneSpec()` — calibrated for a 3 s tick, owner
`MaxSpeed≈20`, fire range comfortably inside the Near AOI window:

| field          | value      | note |
|----------------|-----------|------|
| `Damage`       | 8         | per-tick laser-equivalent, weaker than a missile's 30 one-shot |
| `HP`           | 20        | fragile |
| `FireRange`    | 60        | weapon reach |
| `StandoffRange`| 50        | brakes here, < FireRange so it shoots while holding |
| `Speed`        | 60        | units/s, 3× owner so it keeps up & orbits |
| `Accel`        | 30        | units/s² |
| `TurnRate`     | π         | 180°/s |
| `StrafeK`      | 0.6       | SP `0.6·acceleration` |
| `FrictionK`    | 0.1       | SP `0.1·speed` |
| `TTL`          | 120 s     | self-destruct fence |

## 7. Transport

WS patch carries a drone diff (`dronesAdded/Updated/Removed`) within AOI
and one-frame `droneImpacts`, identical in shape to the missile
contract. The SPA renders drones as small dots near owner/target and
flashes impacts. `GET /api/state` is unchanged (drones are not in the
full HTTP snapshot — they ride the WS delta like missiles).

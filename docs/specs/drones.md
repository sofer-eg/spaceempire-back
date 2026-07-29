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
above the missile id `50`). `recall-drones {shipID}` returns **one unit
per still-alive drone** owned by that ship to its cargo.

**Since TASK-147 the launch handler owns no cargo.** It only routes and
maps, mirroring `install-jammer`:

1. validate request,
2. send `LaunchDroneCommand{PlayerID, ShipID, Target, Count, GoodsType: 51}`
   to the sector worker — the handler owns the goods constant so the
   sector package stays free of the catalog,
3. wait for ack (`AckTimeout`) and map the outcome:
   `ErrShipNotFound` → 404, `ErrForbidden` → 403, `ErrShipDocked` → 400,
   `ErrEquipmentRequired` → 422, `ErrDroneCapReached` → 422,
   `ErrInvalidAttackTarget` → 400, `cargo.ErrInsufficientQuantity` → 400
   "not enough drones in cargo", `cargo.ErrGoodsTypeNotFound` → 500,
   `ErrOrdnanceUnavailable` → 503, ack timeout → 504 with **no
   compensation** (see §2.1).

**Since TASK-152 the recall handler owns no cargo either.** It routes
`RecallDronesCommand{PlayerID, ShipID, GoodsType: 51}` and maps the
outcome (`ErrShipNotFound` → 404, `ErrForbidden` → 403,
`ErrOrdnanceUnavailable` → 503, ack timeout → 504 with no compensation);
the credit happens inside the worker, see §2.2. `api.DroneCargo` and the
handler's `refundDrones` are gone with it — the HTTP layer now owns no
drone cargo operation at all.

### 2.1 Atomicity of the salvo (TASK-147)

**Invariant: the ammunition debit and every drone INSERT commit in ONE
transaction.** `LaunchDroneCommand.apply` clamps the salvo to what
`up_drone_control` still allows (`toSpawn = min(Count, level - live)`),
builds the drones, and hands the whole slice to `sector.Ordnance`
(app-side adapter over `database.TxManager` + `cargo.ConsumeIn` +
`dronesRepo.WithExecutor(tx).Create` per drone). Only on success are they
inserted into the sector's RAM.

`Ordnance` is the **only** launch path. A worker built without one
(`WithOrdnance` lost in a refactor) refuses the command with
`ErrOrdnanceUnavailable` → 503 instead of spawning drones nobody paid
for. Consequently `DroneRepo` carries only `BatchUpdate`/`Delete` —
launches never touch it.

This replaced a `Consume(count)`-before-`Send` / `Refund`-on-failure
orchestration in the handler, which leaked free drones: `AckTimeout` is
only `TickInterval + 1s`, so a delayed tick made the handler refund and
answer 504 while the command was still in the inbox and applied normally
a moment later — ammunition returned, drones flying.

Consequences of the new invariant:
- A 504 means "outcome unknown", not "nothing happened": the player
  checks the hold and retries only if no drones appeared.
- **Partial spawn is gone as a class.** `Spawned` is either `toSpawn` or
  (on error) zero, so the `Count - Spawned` remainder refund the handler
  used to do has nothing left to do. The old mid-salvo `break` on a
  failing INSERT is gone with it.
- **The cap, not the request, is what gets billed** — a deliberate,
  strictly milder behaviour change. The cap is known only inside the
  worker, so billing `Count` would make the player pay for drones the cap
  refuses. Asking for 5 with a level-2 module and 3 units in the hold
  used to fail the handler's `Consume(5)` outright (400, zero drones);
  now 2 launch and exactly 2 are charged.
- Too little ammunition for the **clamped** salvo rejects the whole
  launch (400): nothing charged, nothing spawned.

### 2.2 Atomicity of the recall (TASK-152)

**Invariant: the drone DELETEs and the cargo credit commit in ONE
transaction.** `RecallDronesCommand.apply` collects the ids of the live
drones owned by the ship, hands them to `sector.Ordnance.RecallDrones`
(same app-side adapter, `dronesRepo.WithExecutor(tx).Delete` per id +
`cargo.RefundIn`), and only then removes them from the sector's RAM.

`Ordnance` is the **only** recall path: a worker built without one
refuses with `ErrOrdnanceUnavailable` → 503 rather than deleting drones
with nobody to credit the player (the nil-implementation doctrine of
TASK-144, read backwards). That refusal comes **before** the empty-set
short-circuit, so a misconfigured worker answers the same way whether or
not the ship has drones out; a recall that finds no live drones on a
properly wired worker is a no-op and never opens a transaction.

This replaced a worker-deletes / handler-`Refund`s-after-the-ack
orchestration with the mirror image of the launch hole: `AckTimeout` is
only `TickInterval + 1s`, so a delayed tick made the handler answer 504
and walk away while the command applied a moment later — drones deleted,
nothing credited, the consumable simply lost.

Consequences:
- A 504 means "outcome unknown": drones and hold agree either way.
- **The rows deleted, not the RAM entries, are what gets credited.** A
  drone whose row is already gone (`ErrDroneNotFound`) deletes as a
  no-op inside the transaction, is worth **nothing**, and is still
  cleared from RAM. Any other delete error rolls the whole transaction
  back: nothing deleted, nothing credited, the drones keep flying.
- **RAM changes only after the commit.** The tick goroutine is the sole
  writer and is parked in the call for its duration, so the collected
  ids cannot go stale. A `RepoTimeout` deadline (COMMIT possibly in
  flight) is therefore treated as a failure: the drones keep flying,
  logged at ERROR. That residue is self-correcting **in the ledger** — a
  retried recall finds the rows gone, clears RAM and credits nothing, so
  the player is never paid twice. It is **not** self-correcting on the
  battlefield: if the COMMIT did land, the player holds the credited
  units *and* N still-firing ghost drones until their TTL (or the next
  restart, whose `LoadAll` finds no rows), so he can field up to 2N
  drones for the price of N. Hence ERROR, not a debug line. The opposite
  choice — clearing RAM on an ambiguous outcome — would resurrect
  paid-for drones from their surviving rows at the next cold start.
- **A shortfall is logged.** `credited < len(ids)` emits a WARN with both
  counts: it is what confirms (or refutes) an earlier "outcome in doubt"
  ERROR, and the only trace there would be if drone rows ever started
  vanishing for a real reason (out-of-band DELETE, cascade).
- The DB time is bounded by `Config.RepoTimeout` and charged to the
  drain's DB budget, exactly like a launch.
- **The credit skips the capacity check** (`cargo.RefundIn`, not `Add`):
  it must not be refusable, or the transaction would delete drones it
  cannot pay for. Since TASK-156 the amount is *sized* before the deletes
  instead (`cargo.FitsIn`), so nothing has to be refused — see §2.3.

### 2.3 Partial recall by free hold space (TASK-156)

**Invariant: a recall never takes the hold past `cargobay`, and never
strands a drone permanently.**

The launch side's "the units fitted here a moment ago" premise does not
hold in this direction: a whole drone TTL can pass between launch and
recall, and the ship can dock, sell, buy and fill the freed space
meanwhile. Crediting unconditionally was an exploit — launch N drones,
refill the freed space, recall, repeat — carrying arbitrarily more than
`cargobay`.

Policy (chosen by the owner over "refuse the whole recall" and "allow
overflow, force-drop later"): **credit what fits, leave the rest
flying.**

- Inside the recall transaction `cargo.FitsIn(hold, 51, len(ids))`
  answers how many whole drone units still fit (`(capacity - used) /
  space`, clamped to `[0, len(ids)]`; a weightless good is unbounded).
  Only that many ids are deleted and credited.
- `sector.RecallOutcome{Removed, Credited}` reports both sides: `Removed`
  is what the worker clears from RAM (the un-recalled drones stay in
  `s.drones` and on the radar), `Credited` is what the player is paid.
  `Credited < len(Removed)` still means ghost rows, and still logs the
  WARN above.
- A full hold recalls nothing and is **not** an error: the ack is 200 with
  `recalled: 0, left: N`. The drone is waiting, not lost — the player
  frees space and recalls again. That is what keeps "no state where a ship
  can never recall its drones" true.
- The SPA journals the outcome (`front/src/recallDrones.ts`): a partial
  recall is a WARN line naming how many stayed out, because the button is
  otherwise silent about it.

## 2.4 Point defence (TASK-112)

С TASK-112 дрон, у которого нет живой корабельной цели, перехватывает входящую
**враждебную** торпеду (`acquireDroneTorpedo`, тот же радиус захвата, что для
кораблей). Корабли в приоритете — перехват занимает простаивающие тики. Урон
только по HP, реап торпеды остаётся в `tickTorpedos`. Полное описание, включая
правило враждебности (автоматика гейтится, приказ — нет), — в
`point_defense.md`.

## 3. Persistence (immediate, unlike missiles)

Drones are **persistent state** (acceptance criterion: "при рестарте
сервера дроны восстанавливаются"). Unlike missiles (reconstructable, RAM
only) the drone lifecycle writes to the `drones` table:

- **immediate INSERT** on launch (one row per drone) — inside the
  ammunition transaction, see §2.1,
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
